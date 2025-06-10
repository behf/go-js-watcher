package database

import (
	"log"
	"os"
	"path/filepath"

	"go-js-watcher/models" // Import your models package

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger" // Import gorm logger for better control
)

var DB *gorm.DB // Global variable to hold the database connection

// Init initializes the database connection and runs migrations.
func Init() {
	var err error

	// Ensure the base directory for the database file exists
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to get executable path: %v", err)
	}
	exeDir := filepath.Dir(exePath)
	dbPath := filepath.Join(exeDir, "watcher.db")

	DB, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		// Optional: Configure GORM logger for better debugging
		Logger: logger.Default.LogMode(logger.Info), // Log SQL queries and info
	})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	log.Println("Database connection established.")

	// AutoMigrate will create/update tables based on your models
	err = DB.AutoMigrate(&models.WatchedUrl{}, &models.ChangeEvent{})
	if err != nil {
		log.Fatalf("Failed to auto migrate database: %v", err)
	}

	log.Println("Database migrations completed.")
}
