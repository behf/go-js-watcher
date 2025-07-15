package services

import (
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"go-js-watcher/database" // Import your database package
	"go-js-watcher/models"   // Import your models package

	"github.com/sergi/go-diff/diffmatchpatch" // Character-level diffing
	"gorm.io/gorm"

	// Telegram Bot API package
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// sendTelegramNotification sends a simple notification to Telegram.
func sendTelegramNotification(botToken, chatID, url, diffLink string, isDowntimeAlert bool) {
	if botToken == "" || chatID == "" {
		log.Println("Telegram credentials not provided to notification function. Skipping notification.")
		return
	}

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Printf("Failed to create Telegram bot API: %v", err)
		return
	}

	var messageText string
	if isDowntimeAlert {
		messageText = fmt.Sprintf("<b>Downtime Alert:</b> URL %s appears to be down.", html.EscapeString(url))
	} else {
		messageText = fmt.Sprintf("<b>Change detected in:</b> %s\n\n", html.EscapeString(url))
		if diffLink != "" {
			messageText += fmt.Sprintf("View details on the dashboard:\n\n%s", html.EscapeString(diffLink))
		} else {
			messageText += "View details on the dashboard."
		}
	}

	// Telegram chat ID must be an int64. Parse it.
	parsedChatID, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		log.Printf("Invalid Telegram Chat ID '%s': %v", chatID, err)
		return
	}

	msg := tgbotapi.NewMessage(parsedChatID, messageText)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.DisableWebPagePreview = false

	_, err = bot.Send(msg)
	if err != nil {
		log.Printf("Failed to send Telegram notification for %s: %v", url, err)
	} else {
		log.Printf("Telegram notification sent for %s", url)
	}
}

func CheckURLForChanges(urlID uint, diffViewBaseURL, botToken, chatID string) string {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered in CheckURLForChanges: %v", r)
		}
	}()
	db := database.DB

	log.Printf("Checking URL ID: %d", urlID)
	var urlEntry models.WatchedUrl
	if result := db.First(&urlEntry, urlID); result.Error != nil {
		log.Printf("Error fetching URL ID %d: %v", urlID, result.Error)
		if result.Error == gorm.ErrRecordNotFound {
			return fmt.Sprintf("URL with ID %d not found.", urlID)
		}
		return fmt.Sprintf("Error fetching URL ID %d: %v", urlID, result.Error)
	}
	log.Printf("Successfully fetched URL ID %d: %s", urlID, urlEntry.URL)

	const maxRetries = 3
	const baseBackoff = 2 * time.Second
	var lastErr error

	client := &http.Client{
		Timeout: 20 * time.Second,
	}

	var resp *http.Response
	var err error

	for i := 0; i < maxRetries; i++ {
		lastErr = err
		req, err := http.NewRequest("GET", urlEntry.URL, nil)
		if err != nil {
			urlEntry.Status = fmt.Sprintf("Failed to create request: %v", err)
			db.Save(&urlEntry)
			log.Printf("Error creating request for %s: %v", urlEntry.URL, err)
			return urlEntry.Status
		}
		req.Header.Set("User-Agent", "JS-Watcher-Bot/1.0 (Go)")

		resp, err = client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			break // Success
		}

		if resp != nil {
			resp.Body.Close() // Close body on non-200 responses
		}

		lastErr = err
		backoff := baseBackoff * time.Duration(1<<i) // Exponential backoff
		log.Printf("Attempt %d/%d for %s failed: %v. Retrying in %v...", i+1, maxRetries, urlEntry.URL, err, backoff)
		time.Sleep(backoff)
	}

	if err != nil {
		urlEntry.Status = fmt.Sprintf("Failed after %d retries: %v", maxRetries, lastErr)
		db.Save(&urlEntry)
		log.Printf("Error fetching %s after multiple retries: %v", urlEntry.URL, lastErr)
		sendTelegramNotification(botToken, chatID, urlEntry.URL, "", true)
		return urlEntry.Status
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		urlEntry.Status = fmt.Sprintf("HTTP Error %d: %s", resp.StatusCode, resp.Status)
		db.Save(&urlEntry)
		log.Printf("HTTP Error for %s: %d %s", urlEntry.URL, resp.StatusCode, resp.Status)
		return urlEntry.Status
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		urlEntry.Status = fmt.Sprintf("Failed to read response body: %v", err)
		db.Save(&urlEntry)
		log.Printf("Error reading body for %s: %v", urlEntry.URL, err)
		return urlEntry.Status
	}
	currentContent := string(bodyBytes)

	now := time.Now().UTC()
	urlEntry.LastChecked = &now // Always update LastChecked

	if urlEntry.LastContent == "" {
		urlEntry.LastContent = currentContent
		urlEntry.Status = "Monitoring"
		db.Save(&urlEntry)
		log.Printf("Started watching %s. Initial content stored.", urlEntry.URL)
		return fmt.Sprintf("Started watching %s. Initial content stored.", urlEntry.URL)
	}

	if currentContent != urlEntry.LastContent {
		dmp := diffmatchpatch.New()
		diffs := dmp.DiffMain(urlEntry.LastContent, currentContent, true)
		dmp.DiffCleanupSemantic(diffs)
		htmlDiff := dmp.DiffPrettyHtml(diffs)

		newChange := models.ChangeEvent{
			URLID:      urlEntry.ID,
			DiffText:   htmlDiff,
			DetectedAt: now,
		}

		if result := db.Create(&newChange); result.Error != nil {
			log.Printf("Error saving change event for %s: %v", urlEntry.URL, result.Error)
			urlEntry.Status = fmt.Sprintf("Change detected, but failed to save diff: %v", result.Error)
			db.Save(&urlEntry)
			return urlEntry.Status
		}

		diffLink := ""
		if diffViewBaseURL != "" {
			diffLink = fmt.Sprintf("%s/diff/%d", diffViewBaseURL, newChange.ID)
		}
		sendTelegramNotification(botToken, chatID, urlEntry.URL, diffLink, false)

		urlEntry.LastContent = currentContent
		urlEntry.Status = fmt.Sprintf("Change detected at %s", now.Format("2006-01-02 15:04 UTC"))
	} else {
		urlEntry.Status = "No changes"
	}

	if result := db.Save(&urlEntry); result.Error != nil {
		log.Printf("Error updating URL status for %s: %v", urlEntry.URL, result.Error)
		return fmt.Sprintf("Checked %s: %s (DB update error: %v)", urlEntry.URL, urlEntry.Status, result.Error)
	}

	log.Printf("Checked %s: %s", urlEntry.URL, urlEntry.Status)
	return fmt.Sprintf("Checked %s: %s", urlEntry.URL, urlEntry.Status)
}
