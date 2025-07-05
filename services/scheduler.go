package services

import (
	"log"
	"time"

	"go-js-watcher/database" // Import your database package
	"go-js-watcher/models"   // Import your models package

	"github.com/robfig/cron/v3" // The cron scheduler library
)

// StartScheduler initializes and starts the periodic URL checking.
// The `diffViewBaseURL` parameter is needed to construct the full link for Telegram notifications.
func StartScheduler(diffViewBaseURL, botToken, chatID string) {
	c := cron.New()

	c.AddFunc("@every 1m", func() {
		log.Println("Scheduler: Running periodic check for URLs due...")
		now := time.Now().UTC()

		var urlsToProcess []models.WatchedUrl
		result := database.DB.Where("is_active = ?", true).Find(&urlsToProcess)
		if result.Error != nil {
			log.Printf("Scheduler: Error fetching URLs: %v", result.Error)
			return
		}

		for _, urlEntry := range urlsToProcess {
			if urlEntry.LastChecked == nil {
				log.Printf("Scheduler: URL '%s' never checked, scheduling first check.", urlEntry.URL)
				// Pass credentials here
				go CheckURLForChanges(urlEntry.ID, diffViewBaseURL, botToken, chatID)
				continue
			}

			timeSinceLastCheck := now.Sub(*urlEntry.LastChecked)
			if timeSinceLastCheck.Minutes() >= 1 {
				log.Printf("Scheduler: URL '%s' due for check (last checked %v ago), scheduling.", urlEntry.URL, timeSinceLastCheck.Round(time.Second))
				// Pass credentials here
				go CheckURLForChanges(urlEntry.ID, diffViewBaseURL, botToken, chatID)
			}
		}
	})

	c.Start()
	log.Println("Periodic URL checking scheduler started.")
}
