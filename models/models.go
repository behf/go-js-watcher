package models

import (
	"time"

	"gorm.io/gorm"
)

// WatchedUrl represents a URL being watched in the database.
type WatchedUrl struct {
	gorm.Model             // Provides ID, CreatedAt, UpdatedAt, DeletedAt
	URL             string `gorm:"unique;not null"`
	IntervalSeconds int    `gorm:"not null;default:300"`
	LastContent     string
	LastChecked     *time.Time    // Use pointer to allow nil for initial state
	Status          string        `gorm:"default:'Pending'"`
	IsActive        bool          `gorm:"default:true"`
	Changes         []ChangeEvent `gorm:"foreignKey:URLID;constraint:OnDelete:CASCADE;"` // One-to-many relationship
}

// ChangeEvent represents a detected change for a WatchedUrl.
type ChangeEvent struct {
	gorm.Model           // Provides ID, CreatedAt, UpdatedAt, DeletedAt
	DiffText   string    `gorm:"not null"`
	DetectedAt time.Time `gorm:"not null"`
	URLID      uint      `gorm:"not null"`      // Foreign key to WatchedUrl
	IsRead     bool      `gorm:"default:false"` // NEW FIELD: Tracks if the change has been "read"
}
