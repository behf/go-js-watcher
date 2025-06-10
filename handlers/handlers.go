package handlers

import (
	"html/template" // For HTML templating
	"io"
	"log"
	"net/http" // Standard HTTP package
	"strconv"  // For string to integer conversion

	// For time operations
	"go-js-watcher/database" // Import your database package
	"go-js-watcher/models"   // Import your models package
	"go-js-watcher/services" // Import your services package (for CheckURLForChanges)

	"github.com/gorilla/sessions" // For session management (similar to Flask's session)
	"github.com/labstack/echo/v4" // The Echo web framework
	"gorm.io/gorm"
)

const (
	sessionName   = "js-watcher-session"
	sessionKey    = "logged_in"
	flashMessages = "flash_messages"
)

var (
	// Store for gorilla/sessions. Replace with a production-ready store in a real app.
	// For simplicity, using a cookie store, but a file store or Redis store is better for production.
	// Make sure this key is truly secret and loaded from environment variables in main.go
	store *sessions.CookieStore

	// These will be set in main.go
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
		// Ordering by DetectedAt DESC and ID DESC ensures stable ordering for "last 5"
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

	return c.Render(http.StatusOK, "dashboard.html", echo.Map{
		"WatchedUrls": urls,
		"Flashes":     GetFlashes(c),
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

	// Immediately queue a check for the new URL in a goroutine
	// Pass credentials here
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

	// GORM's cascade delete should handle ChangeEvents if configured on the model.
	// We configured `cascade:"all, delete-orphan"` on WatchedUrl.Changes.
	if result := database.DB.Delete(&urlToDelete); result.Error != nil {
		Flash(c, "Failed to remove URL: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	Flash(c, "Stopped watching "+urlToDelete.URL+".")
	return c.Redirect(http.StatusFound, "/dashboard")
}

// ViewDiff renders the diff view for a specific change event.
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

	// Mark the change event as read
	if !changeEvent.IsRead {
		changeEvent.IsRead = true
		if result := database.DB.Save(&changeEvent); result.Error != nil {
			// Log the error but don't prevent showing the diff
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

// HTMLTemplateRenderer is a custom renderer for Echo
type HTMLTemplateRenderer struct {
	Templates *template.Template
}

// Render renders a template document
func (t *HTMLTemplateRenderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.Templates.ExecuteTemplate(w, name, data)
}
