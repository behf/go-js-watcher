package database

import (
	"log"
	"os"
	"path/filepath"

	"go-js-watcher/models" // Import your models package

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger" // Import gorm logger
)

var DB *gorm.DB // Global variable to hold the database connection

// Init initializes the database connection and runs migrations.
func Init() {
	var err error

	err = os.MkdirAll("./data", 0755)
	if err != nil {
		log.Fatal("Could not create data directory:", err)
	}
	// Use a relative path for the database file
	dbPath := filepath.Join("./data", "watcher.db")

	DB, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		// Change LogMode to Silent to disable SQL logs
		// Or logger.Error to only log errors related to GORM operations
		Logger: logger.Default.LogMode(logger.Error),
	})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	log.Println("Database connection established.")

	err = DB.AutoMigrate(&models.WatchedUrl{}, &models.ChangeEvent{}, &models.URLGroup{})

	if err != nil {
		log.Fatalf("Failed to auto migrate database: %v", err)
	}

	log.Println("Database migrations completed.")
}
