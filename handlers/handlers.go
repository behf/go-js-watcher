package handlers

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
	BaseURL     string
)

func SetSessionStore(secretKey []byte) {
	store = sessions.NewCookieStore(secretKey)
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
	}
}

func Flash(c echo.Context, message string) {
	session, _ := store.Get(c.Request(), sessionName)
	messages := session.Flashes(flashMessages)
	messages = append(messages, message)
	session.AddFlash(messages, flashMessages)
	session.Save(c.Request(), c.Response())
}

func GetFlashes(c echo.Context) []string {
	session, _ := store.Get(c.Request(), sessionName)
	flashes := session.Flashes(flashMessages)
	session.Save(c.Request(), c.Response())
	var messages []string
	for _, f := range flashes {
		if msg, ok := f.(string); ok {
			messages = append(messages, msg)
		} else if msgArr, ok := f.([]interface{}); ok {
			for _, m := range msgArr {
				if mStr, ok := m.(string); ok {
					messages = append(messages, mStr)
				}
			}
		}
	}
	return messages
}

func AuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		session, _ := store.Get(c.Request(), sessionName)
		if auth, ok := session.Values[sessionKey].(bool); !ok || !auth {
			return c.Redirect(http.StatusFound, "/login")
		}
		return next(c)
	}
}

func LoginGet(c echo.Context) error {
	return c.Render(http.StatusOK, "login.html", echo.Map{
		"Flashes": GetFlashes(c),
	})
}

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

func Logout(c echo.Context) error {
	session, _ := store.Get(c.Request(), sessionName)
	session.Values[sessionKey] = false
	session.Options.MaxAge = -1
	session.Save(c.Request(), c.Response())
	return c.Redirect(http.StatusFound, "/login")
}

func Dashboard(c echo.Context) error {
	var urls []models.WatchedUrl
	result := database.DB.Preload("Changes", func(db *gorm.DB) *gorm.DB {
		return db.Order("detected_at DESC, id DESC").Limit(5)
	}).Where("group_id IS NULL").Find(&urls)

	var urlGroups []models.URLGroup
	result = database.DB.Preload("URLs").Preload("URLs.Changes", func(db *gorm.DB) *gorm.DB {
		return db.Order("detected_at DESC, id DESC").Limit(5)
	}).Find(&urlGroups)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
		} else {
			Flash(c, "Error loading URLs: "+result.Error.Error())
		}
	}

	for i := range urls {
		if urls[i].LastChecked != nil {
			localTime := urls[i].LastChecked.Local()
			urls[i].LastChecked = &localTime
		}
		for j := range urls[i].Changes {
			urls[i].Changes[j].DetectedAt = urls[i].Changes[j].DetectedAt.Local()
		}
	}

	var totalURLs int64
	database.DB.Model(&models.WatchedUrl{}).Count(&totalURLs)

	var urlsWithUnreadChanges int64
	database.DB.Model(&models.WatchedUrl{}).
		Joins("JOIN change_events ON watched_urls.id = change_events.url_id").
		Where("change_events.is_read = ?", false).
		Distinct("watched_urls.id").
		Count(&urlsWithUnreadChanges)

	var averageIntervalSeconds float64
	row := database.DB.Model(&models.WatchedUrl{}).Select("COALESCE(AVG(interval_seconds), 0)").Row()
	row.Scan(&averageIntervalSeconds)

	now := time.Now().UTC()
	twentyFourHoursAgo := now.Add(-24 * time.Hour)
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)

	var changes24h, changes7d int64
	database.DB.Model(&models.ChangeEvent{}).Where("detected_at >= ?", twentyFourHoursAgo).Count(&changes24h)
	database.DB.Model(&models.ChangeEvent{}).Where("detected_at >= ?", sevenDaysAgo).Count(&changes7d)

	return c.Render(http.StatusOK, "dashboard.html", echo.Map{
		"WatchedUrls":      urls,
		"URLGroups":        urlGroups,
		"Flashes":          GetFlashes(c),
		"TotalURLs":        totalURLs,
		"URLsWithUnread":   urlsWithUnreadChanges,
		"AvgCheckInterval": fmt.Sprintf("%.1f seconds", averageIntervalSeconds),
		"ChangesLast24h":   changes24h,
		"ChangesLast7d":    changes7d,
	})
}

func AddURL(c echo.Context, botToken, chatID string) error {
	url := c.FormValue("url")
	intervalStr := c.FormValue("interval")

	if url == "" {
		Flash(c, "URL is required.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var interval int
	if intervalStr == "" {
		interval = 300
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
		Status:          " Scheduled for first check",
	}

	if result := database.DB.Create(&newURL); result.Error != nil {
		Flash(c, "Failed to add URL: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	Flash(c, "Started watching "+url+".")

	go services.CheckURLForChanges(newURL.ID, BaseURL, botToken, chatID)

	return c.Redirect(http.StatusFound, "/dashboard")
}

func RemoveURL(c echo.Context) error {
	urlIDStr := c.FormValue("id")
	urlID, err := strconv.ParseUint(urlIDStr, 10, 32)
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

	var prevChangeID, nextChangeID uint

	var prevChangeEvent models.ChangeEvent
	if result := database.DB.
		Where("url_id = ? AND id < ?", changeEvent.URLID, changeEvent.ID).
		Order("id DESC").Limit(1).First(&prevChangeEvent); result.Error == nil {
		prevChangeID = prevChangeEvent.ID
	}

	var nextChangeEvent models.ChangeEvent
	if result := database.DB.
		Where("url_id = ? AND id > ?", changeEvent.URLID, changeEvent.ID).
		Order("id ASC").Limit(1).First(&nextChangeEvent); result.Error == nil {
		nextChangeID = nextChangeEvent.ID
	}

	return c.Render(http.StatusOK, "view_diff.html", echo.Map{
		"ChangeEvent":  changeEvent,
		"WatchedURL":   watchedURL,
		"DiffContent":  template.HTML(changeEvent.DiffText),
		"PrevChangeID": prevChangeID,
		"NextChangeID": nextChangeID,
	})
}

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
		newInterval = 300
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

	if existingURL.URL != newURL {
		var conflictURL models.WatchedUrl
		if result := database.DB.Unscoped().Where("url = ?", newURL).First(&conflictURL); result.Error == nil && conflictURL.ID != existingURL.ID {
			Flash(c, "Another URL entry with this URL already exists.")
			return c.Redirect(http.StatusFound, fmt.Sprintf("/edit_url/%d", urlID))
		} else if result.Error != gorm.ErrRecordNotFound {
			Flash(c, "Database error checking new URL for conflict: "+result.Error.Error())
			return c.Redirect(http.StatusFound, fmt.Sprintf("/edit_url/%d", urlID))
		}
	}

	existingURL.URL = newURL
	existingURL.IntervalSeconds = newInterval

	if result := database.DB.Save(&existingURL); result.Error != nil {
		Flash(c, "Failed to update URL: "+result.Error.Error())
		return c.Redirect(http.StatusFound, fmt.Sprintf("/edit_url/%d", urlID))
	}

	Flash(c, "URL updated successfully.")
	return c.Redirect(http.StatusFound, "/dashboard")
}

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
			Flash(c, "URL not irrespective.")
		} else {
			Flash(c, "Database error finding URL: "+result.Error.Error())
		}
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	urlEntry.IsActive = !urlEntry.IsActive

	if result := database.DB.Save(&urlEntry); result.Error != nil {
		Flash(c, "Failed to toggle URL status: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	if urlEntry.IsActive {
		Flash(c, fmt.Sprintf("Started watching %s again.", urlEntry.URL))
		go services.CheckURLForChanges(urlEntry.ID, BaseURL, os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID"))
	} else {
		Flash(c, fmt.Sprintf("Stopped watching %s.", urlEntry.URL))
	}

	return c.Redirect(http.StatusFound, "/dashboard")
}

func AllChangesGet(c echo.Context) error {
	urlIDStr := c.Param("url_id")
	urlID, err := strconv.ParseUint(urlIDStr, 10, 32)
	if err != nil {
		Flash(c, "Invalid URL ID.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var watchedURL models.WatchedUrl
	if result := database.DB.First(&watchedURL, urlID); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			Flash(c, "URL not found.")
			return c.Redirect(http.StatusFound, "/dashboard")
		}
		Flash(c, "Database error finding URL: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var changes []models.ChangeEvent
	if result := database.DB.Where("url_id = ?", urlID).Order("detected_at DESC, id DESC").Find(&changes); result.Error != nil {
		Flash(c, "Database error retrieving changes: "+result.Error.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	for i := range changes {
		changes[i].DetectedAt = changes[i].DetectedAt.Local()
	}

	return c.Render(http.StatusOK, "all_changes.html", echo.Map{
		"WatchedURL": watchedURL,
		"Changes":    changes,
		"Flashes":    GetFlashes(c),
	})
}

func ExtractJS(c echo.Context) error {
	url := c.FormValue("url")
	tool := c.FormValue("tool")

	if url == "" {
		Flash(c, "URL is required.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var cmd *exec.Cmd
	switch tool {
	case "getJS":
		cmd = exec.Command("getJS", "-url", url)
	case "jsxtract":
		cmd = exec.Command("jsxtract", url)
	default:
		Flash(c, "Invalid tool selected.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	output, err := cmd.Output()
	if err != nil {
		Flash(c, "Error executing tool: "+err.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	jsFiles := strings.Split(string(output), "\n")
	var cleanedJSFiles []string
	for _, file := range jsFiles {
		if strings.TrimSpace(file) != "" {
			cleanedJSFiles = append(cleanedJSFiles, strings.TrimSpace(file))
		}
	}

	return c.Render(http.StatusOK, "extract_results.html", echo.Map{
		"SourceURL": url,
		"GroupName": "Scripts from " + url,
		"JSFiles":   cleanedJSFiles,
		"Flashes":   GetFlashes(c),
	})
}

func AddExtractedJS(c echo.Context, botToken, chatID string) error {
	sourceURL := c.FormValue("source_url")
	groupName := c.FormValue("group_name")
	jsFiles := c.Request().Form["js_files"]
	intervalStr := c.FormValue("interval")

	interval, err := strconv.Atoi(intervalStr)
	if err != nil || interval <= 0 {
		Flash(c, "Invalid interval. Must be a positive number.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	if len(jsFiles) == 0 {
		Flash(c, "No JS files selected.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var urlGroup models.URLGroup
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// Check if a URLGroup with the same sourceURL already exists
		result := tx.Where("source_url = ?", sourceURL).First(&urlGroup)
		if result.Error == nil {
			// Group exists, update name if provided
			if groupName != "" && groupName != urlGroup.Name {
				urlGroup.Name = groupName
				if result := tx.Save(&urlGroup); result.Error != nil {
					return result.Error
				}
			}
		} else if result.Error == gorm.ErrRecordNotFound {
			// Create new group if none exists
			urlGroup = models.URLGroup{
				Name:      groupName,
				SourceURL: sourceURL,
			}
			if result := tx.Create(&urlGroup); result.Error != nil {
				return result.Error
			}
		} else {
			return result.Error
		}

		// Add new JS files as WatchedUrl entries
		for _, jsFile := range jsFiles {
			// Check if the URL is already being watched
			var existingURL models.WatchedUrl
			if result := tx.Unscoped().Where("url = ?", jsFile).First(&existingURL); result.Error == nil {
				continue // Skip if URL already exists
			} else if result.Error != gorm.ErrRecordNotFound {
				return result.Error
			}

			newURL := models.WatchedUrl{
				URL:             jsFile,
				IntervalSeconds: interval,
				Status:          "Scheduled for first check",
				GroupID:         &urlGroup.ID,
			}
			if result := tx.Create(&newURL); result.Error != nil {
				return result.Error
			}
		}
		return nil
	})

	if err != nil {
		Flash(c, "Failed to create or update URL group or add URLs: "+err.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	// Trigger checks for newly added URLs
	for _, jsFile := range jsFiles {
		var newURL models.WatchedUrl
		if result := database.DB.Where("url = ? AND group_id = ?", jsFile, urlGroup.ID).First(&newURL); result.Error == nil {
			go services.CheckURLForChanges(newURL.ID, BaseURL, botToken, chatID)
		}
	}

	Flash(c, fmt.Sprintf("Added %d JS files to be watched under the group '%s'.", len(jsFiles), groupName))
	return c.Redirect(http.StatusFound, "/dashboard")
}

func RemoveGroup(c echo.Context) error {
	groupIDStr := c.FormValue("group_id")
	groupID, err := strconv.ParseUint(groupIDStr, 10, 32)
	if err != nil {
		Flash(c, "Invalid group ID.")
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	var group models.URLGroup
	if result := database.DB.First(&group, groupID); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			Flash(c, "Group not found.")
		} else {
			Flash(c, "Database error finding group: "+result.Error.Error())
		}
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// Delete all associated WatchedUrl records (and their ChangeEvent records due to CASCADE)
		if result := tx.Unscoped().Where("group_id = ?", groupID).Delete(&models.WatchedUrl{}); result.Error != nil {
			return result.Error
		}

		// Delete the group
		if result := tx.Unscoped().Delete(&group); result.Error != nil {
			return result.Error
		}
		return nil
	})

	if err != nil {
		Flash(c, "Failed to remove group and its URLs: "+err.Error())
		return c.Redirect(http.StatusFound, "/dashboard")
	}

	Flash(c, "Group '"+group.Name+"' and all its URLs have been removed.")
	return c.Redirect(http.StatusFound, "/dashboard")
}

type HTMLTemplateRenderer struct {
	Templates *template.Template
}

func (t *HTMLTemplateRenderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.Templates.ExecuteTemplate(w, name, data)
}
