package handlers

import (
	"fmt" // Import fmt for Sprintf
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"go-js-watcher/database"
	"go-js-watcher/models"
	"go-js-watcher/services"

	"io"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const (
	sessionName   = "js-watcher-session"
	sessionKey    = "logged_in"
	flashMessages = "flash_messages"
)

var (
	store *sessions.CookieStore

	AppUsername string
	AppPassword string
	BaseURL     string // Base URL of the application, used for constructing diff links
)

// SetSessionStore initializes the session store. Call this from main.go.
func SetSessionStore(secretKey []byte) {
	store = sessions.NewCookieStore(secretKey)
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7, // 7 days
		HttpOnly: true,
	}
}

// Flash adds a message to the session for displaying on the next request.
func Flash(c echo.Context, message string) {
	session, _ := store.Get(c.Request(), sessionName)
	messages := session.Flashes(flashMessages)
	messages = append(messages, message)
	session.AddFlash(messages, flashMessages) // Add messages back
	session.Save(c.Request(), c.Response())
}

// GetFlashes retrieves and clears flash messages.
func GetFlashes(c echo.Context) []string {
	session, _ := store.Get(c.Request(), sessionName)
	flashes := session.Flashes(flashMessages)
	session.Save(c.Request(), c.Response()) // Save to clear flashes
	var messages []string
	for _, f := range flashes {
		if msg, ok := f.(string); ok {
			messages = append(messages, msg)
		} else if msgArr, ok := f.([]interface{}); ok { // Handle when Flash adds array directly
			for _, m := range msgArr {
				if mStr, ok := m.(string); ok {
					messages = append(messages, mStr)
				}
			}
		}
	}
	return messages
}

// AuthMiddleware checks if the user is logged in.
func AuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		session, _ := store.Get(c.Request(), sessionName)
		if auth, ok := session.Values[sessionKey].(bool); !ok || !auth {
			return c.Redirect(http.StatusFound, "/login")
		}
		return next(c)
	}
}

// Login GET renders the login form.
func LoginGet(c echo.Context) error {
	return c.Render(http.StatusOK, "login.html", echo.Map{
		"Flashes": GetFlashes(c),
	})
}

// Login POST handles login attempts.
func LoginPost(c echo.Context) error {
	username := c.FormValue("username")
	password := c.FormValue("password")

	if username == AppUsername && password == AppPassword {
		session, _ := store.Get(c.Request(), sessionName)
		session.Values[sessionKey] = true
		session.Save(c.Request(), c.Response())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	Flash(c, "Invalid credentials")
	return c.Redirect(http.StatusFound, "/login")
}

// Logout clears the session.
func Logout(c echo.Context) error {
	session, _ := store.Get(c.Request(), sessionName)
	session.Values[sessionKey] = false // Set logged_in to false
	session.Options.MaxAge = -1        // Expire the cookie
	session.Save(c.Request(), c.Response())
	return c.Redirect(http.StatusFound, "/login")
}

// Dashboard renders the main dashboard page.
func Dashboard(c echo.Context) error {
	var urls []models.WatchedUrl
	result := database.DB.Preload("Changes", func(db *gorm.DB) *gorm.DB {
		return db.Order("detected_at DESC, id DESC").Limit(5)
	}).Find(&urls)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			// No URLs found, which is fine
		} else {
			Flash(c, "Error loading URLs: "+result.Error.Error())
		}
	}

	// Convert times to local timezone for display
	for i := range urls {
		if urls[i].LastChecked != nil {
			localTime := urls[i].LastChecked.Local()
			urls[i].LastChecked = &localTime
		}
		for j := range urls[i].Changes {
			urls[i].Changes[j].DetectedAt = urls[i].Changes[j].DetectedAt.Local()
		}
	}

	// --- Calculate Dashboard Metrics ---
	var totalURLs int64
	database.DB.Model(&models.WatchedUrl{}).Count(&totalURLs)

	var urlsWithUnreadChanges int64
	database.DB.Model(&models.WatchedUrl{}).
		Joins("JOIN change_events ON watched_urls.id = change_events.url_id").
		Where("change_events.is_read = ?", false).
		Distinct("watched_urls.id").
		Count(&urlsWithUnreadChanges)

	var avgIntervalSeconds float64
	// Use Raw SQL for AVG to ensure correct float return from database driver
	row := database.DB.Model(&models.WatchedUrl{}).Select("COALESCE(AVG(interval_seconds), 0)").Row()
	row.Scan(&avgIntervalSeconds)

	now := time.Now().UTC()
	twentyFourHoursAgo := now.Add(-24 * time.Hour)
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)

	var changes24h, changes7d int64
	database.DB.Model(&models.ChangeEvent{}).Where("detected_at >= ?", twentyFourHoursAgo).Count(&changes24h)
	database.DB.Model(&models.ChangeEvent{}).Where("detected_at >= ?", sevenDaysAgo).Count(&changes7d)

	return c.Render(http.StatusOK, "dashboard.html", echo.Map{
		"WatchedUrls":      urls,
		"Flashes":          GetFlashes(c),
		"TotalURLs":        totalURLs,
		"URLsWithUnread":   urlsWithUnreadChanges,
		"AvgCheckInterval": fmt.Sprintf("%.1f seconds", avgIntervalSeconds), // Format as string
		"ChangesLast24h":   changes24h,
		"ChangesLast7d":    changes7d,
	})
}

// AddURL handles adding a new URL to watch.
func AddURL(c echo.Context, botToken, chatID string) error {
	url := c.FormValue("url")
	intervalStr := c.FormValue("interval")

	if url == "" {
		Flash(c, "URL is required.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var interval int
	if intervalStr == "" {
		interval = 300 // Default interval
	} else {
		parsedInterval, err := strconv.Atoi(intervalStr)
		if err != nil || parsedInterval <= 0 {
			Flash(c, "Invalid interval. Must be a positive number.")
			return c.Redirect(http.StatusFound, "/dashboard")
		}
		interval = parsedInterval
	}

	var existingURL models.WatchedUrl
	if result := database.DB.Where("url = ?", url).First(&existingURL); result.Error == nil {
		Flash(c, "This URL is already being watched.")
		return c.Redirect(http.StatusFound, "/dashboard")
	} else if result.Error != gorm.ErrRecordNotFound {
		Flash(c, "Database error checking existing URL: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	newURL := models.WatchedUrl{
		URL:             url,
		IntervalSeconds: interval,
		Status:          "Scheduled for first check",
	}

	if result := database.DB.Create(&newURL); result.Error != nil {
		Flash(c, "Failed to add URL: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	Flash(c, "Started watching "+url+".")

	go services.CheckURLForChanges(newURL.ID, BaseURL, botToken, chatID)

	return c.Redirect(http.StatusFound, "/dashboard")
}

// RemoveURL handles removing a watched URL.
func RemoveURL(c echo.Context) error {
	urlIDStr := c.FormValue("id")
	urlID, err := strconv.ParseUint(urlIDStr, 10, 32) // Parse as uint for GORM ID
	if err != nil {
		Flash(c, "Invalid URL ID.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var urlToDelete models.WatchedUrl
	if result := database.DB.First(&urlToDelete, urlID); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			Flash(c, "URL not found.")
		} else {
			Flash(c, "Database error finding URL: "+result.Error.Error())
		}
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	if result := database.DB.Unscoped().Delete(&urlToDelete); result.Error != nil {
		Flash(c, "Failed to remove URL: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	Flash(c, "Stopped watching "+urlToDelete.URL+".")
	return c.Redirect(http.StatusFound, "/dashboard")
}

// ViewDiff renders the diff view for a specific change event and marks it as read.
func ViewDiff(c echo.Context) error {
	eventIDStr := c.Param("event_id")
	eventID, err := strconv.ParseUint(eventIDStr, 10, 32)
	if err != nil {
		return c.String(http.StatusBadRequest, "Invalid event ID")
	}

	var changeEvent models.ChangeEvent
	if result := database.DB.First(&changeEvent, eventID); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return c.String(http.StatusNotFound, "Change event not found.")
		}
		return c.String(http.StatusInternalServerError, "Database error retrieving change event: "+result.Error.Error())
	}

	if !changeEvent.IsRead {
		changeEvent.IsRead = true
		if result := database.DB.Save(&changeEvent); result.Error != nil {
			log.Printf("Error marking change event %d as read: %v", changeEvent.ID, result.Error)
		}
	}

	var watchedURL models.WatchedUrl
	if result := database.DB.First(&watchedURL, changeEvent.URLID); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return c.String(http.StatusNotFound, "Associated URL not found for change event.")
		}
		return c.String(http.StatusInternalServerError, "Database error retrieving associated URL: "+result.Error.Error())
	}

	changeEvent.DetectedAt = changeEvent.DetectedAt.Local()

	return c.Render(http.StatusOK, "view_diff.html", echo.Map{
		"ChangeEvent": changeEvent,
		"WatchedURL":  watchedURL,
		"DiffContent": template.HTML(changeEvent.DiffText),
	})
}

// EditURLGet renders the form to edit an existing URL.
func EditURLGet(c echo.Context) error {
	urlIDStr := c.Param("id")
	urlID, err := strconv.ParseUint(urlIDStr, 10, 32)
	if err != nil {
		Flash(c, "Invalid URL ID.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var urlToEdit models.WatchedUrl
	if result := database.DB.First(&urlToEdit, urlID); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			Flash(c, "URL not found.")
			return c.Redirect(http.StatusFound, "/dashboard")
		}
		Flash(c, "Database error finding URL: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	return c.Render(http.StatusOK, "edit_url.html", echo.Map{
		"URL":     urlToEdit,
		"Flashes": GetFlashes(c),
	})
}

// EditURLPost handles the submission of the edited URL form.
func EditURLPost(c echo.Context, botToken, chatID string) error {
	urlIDStr := c.FormValue("id")
	newURL := c.FormValue("url")
	newIntervalStr := c.FormValue("interval")

	urlID, err := strconv.ParseUint(urlIDStr, 10, 32)
	if err != nil {
		Flash(c, "Invalid URL ID for edit.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	if newURL == "" {
		Flash(c, "URL cannot be empty.")
		return c.Redirect(http.StatusFound, fmt.Sprintf("/edit_url/%d", urlID))
	}

	var newInterval int
	if newIntervalStr == "" {
		newInterval = 300 // Default if empty
	} else {
		parsedInterval, err := strconv.Atoi(newIntervalStr)
		if err != nil || parsedInterval <= 0 {
			Flash(c, "Invalid interval. Must be a positive number.")
			return c.Redirect(http.StatusFound, fmt.Sprintf("/edit_url/%d", urlID))
		}
		newInterval = parsedInterval
	}

	var existingURL models.WatchedUrl
	if result := database.DB.First(&existingURL, urlID); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			Flash(c, "URL to edit not found.")
			return c.Redirect(http.StatusFound, "/dashboard")
		}
		Flash(c, "Database error finding URL for edit: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	// Check if the new URL is already watched by another entry (if URL itself changed)
	if existingURL.URL != newURL {
		var conflictURL models.WatchedUrl
		// Use Unscoped to check against soft-deleted URLs too, for stricter uniqueness
		if result := database.DB.Unscoped().Where("url = ?", newURL).First(&conflictURL); result.Error == nil && conflictURL.ID != existingURL.ID {
			Flash(c, "Another URL entry with this URL already exists.")
			return c.Redirect(http.StatusFound, fmt.Sprintf("/edit_url/%d", urlID))
		} else if result.Error != gorm.ErrRecordNotFound {
			Flash(c, "Database error checking new URL for conflict: "+result.Error.Error())
			return c.Redirect(http.StatusFound, fmt.Sprintf("/edit_url/%d", urlID))
		}
	}

	// Update fields
	existingURL.URL = newURL
	existingURL.IntervalSeconds = newInterval

	if result := database.DB.Save(&existingURL); result.Error != nil {
		Flash(c, "Failed to update URL: "+result.Error.Error())
		return c.Redirect(http.StatusFound, fmt.Sprintf("/edit_url/%d", urlID))
	}

	Flash(c, "URL updated successfully.")
	return c.Redirect(http.StatusFound, "/dashboard")
}

// ToggleURLActive handles enabling/disabling a URL.
func ToggleURLActive(c echo.Context) error {
	urlIDStr := c.FormValue("id")
	urlID, err := strconv.ParseUint(urlIDStr, 10, 32)
	if err != nil {
		Flash(c, "Invalid URL ID.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var urlEntry models.WatchedUrl
	if result := database.DB.First(&urlEntry, urlID); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			Flash(c, "URL not found.")
		} else {
			Flash(c, "Database error finding URL: "+result.Error.Error())
		}
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	urlEntry.IsActive = !urlEntry.IsActive // Toggle the boolean status

	if result := database.DB.Save(&urlEntry); result.Error != nil {
		Flash(c, "Failed to toggle URL status: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	if urlEntry.IsActive {
		Flash(c, fmt.Sprintf("Started watching %s again.", urlEntry.URL))
		// Optionally trigger an immediate check when enabling
		go services.CheckURLForChanges(urlEntry.ID, BaseURL, os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID"))
	} else {
		Flash(c, fmt.Sprintf("Stopped watching %s.", urlEntry.URL))
	}

	return c.Redirect(http.StatusFound, "/dashboard")
}

// AllChangesGet displays all historical change events for a specific URL.
func AllChangesGet(c echo.Context) error {
	urlIDStr := c.Param("url_id")
	urlID, err := strconv.ParseUint(urlIDStr, 10, 32)
	if err != nil {
		Flash(c, "Invalid URL ID.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var watchedURL models.WatchedUrl
	// Fetch the URL itself
	if result := database.DB.First(&watchedURL, urlID); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			Flash(c, "URL not found.")
			return c.Redirect(http.StatusFound, "/dashboard")
		}
		Flash(c, "Database error finding URL: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	// Fetch all changes for this URL, ordered by most recent first
	var changes []models.ChangeEvent
	if result := database.DB.Where("url_id = ?", urlID).Order("detected_at DESC, id DESC").Find(&changes); result.Error != nil {
		Flash(c, "Database error retrieving changes: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	// Convert times to local timezone for display
	for i := range changes {
		changes[i].DetectedAt = changes[i].DetectedAt.Local()
	}

	return c.Render(http.StatusOK, "all_changes.html", echo.Map{
		"WatchedURL": watchedURL, // Pass the URL for context in the template
		"Changes":    changes,    // Pass all changes
		"Flashes":    GetFlashes(c),
	})
}

// HTMLTemplateRenderer is a custom renderer for Echo
type HTMLTemplateRenderer struct {
	Templates *template.Template
}

// Render renders a template document
func (t *HTMLTemplateRenderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.Templates.ExecuteTemplate(w, name, data)
}
