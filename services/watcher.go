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

	// Telegram Bot API
	"github.com/sergi/go-diff/diffmatchpatch" // Character-level diffing
	"gorm.io/gorm"

	// Telegram Bot API package
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// sendTelegramNotification sends a simple notification to Telegram.
// Now accepts botToken and chatID as arguments.
func sendTelegramNotification(botToken, chatID, url, diffLink string) {
	if botToken == "" || chatID == "" {
		log.Println("Telegram credentials not provided to notification function. Skipping notification.")
		return
	}

	bot, err := tgbotapi.NewBotAPI(botToken) // Using tgbotapi as you noted you fixed it to.
	if err != nil {
		log.Printf("Failed to create Telegram bot API: %v", err)
		return
	}

	messageText := fmt.Sprintf("<b>Change detected in:</b> %s\n\n", html.EscapeString(url))
	if diffLink != "" {
		messageText += fmt.Sprintf("View details on the dashboard:\n\n%s", html.EscapeString(diffLink))
	} else {
		messageText += "View details on the dashboard."
	}

	// Telegram chat ID must be an int64. Parse it.
	parsedChatID, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		log.Printf("Invalid Telegram Chat ID '%s': %v", chatID, err)
		return
	}

	msg := tgbotapi.NewMessage(parsedChatID, messageText) // Using tgbotapi.NewMessage
	msg.ParseMode = tgbotapi.ModeHTML
	msg.DisableWebPagePreview = false

	_, err = bot.Send(msg)
	if err != nil {
		log.Printf("Failed to send Telegram notification for %s: %v", url, err)
	} else {
		log.Printf("Telegram notification sent for %s", url)
	}
}

// CheckURLForChanges now accepts botToken and chatID.
func CheckURLForChanges(urlID uint, diffViewBaseURL, botToken, chatID string) string {
	db := database.DB

	var urlEntry models.WatchedUrl
	if result := db.First(&urlEntry, urlID); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return fmt.Sprintf("URL with ID %d not found.", urlID)
		}
		log.Printf("Error fetching URL ID %d: %v", urlID, result.Error)
		return fmt.Sprintf("Error fetching URL ID %d: %v", urlID, result.Error)
	}

	client := &http.Client{
		Timeout: 20 * time.Second,
	}
	req, err := http.NewRequest("GET", urlEntry.URL, nil)
	if err != nil {
		urlEntry.Status = fmt.Sprintf("Failed to create request: %v", err)
		db.Save(&urlEntry)
		log.Printf("Error creating request for %s: %v", urlEntry.URL, err)
		return urlEntry.Status
	}
	req.Header.Set("User-Agent", "JS-Watcher-Bot/1.0 (Go)")

	resp, err := client.Do(req)
	if err != nil {
		urlEntry.Status = fmt.Sprintf("Failed to fetch: %v", err)
		db.Save(&urlEntry)
		log.Printf("Error fetching %s: %v", urlEntry.URL, err)
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
	urlEntry.LastChecked = &now

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
		sendTelegramNotification(botToken, chatID, urlEntry.URL, diffLink) // Pass credentials here

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
