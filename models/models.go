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
	GroupID         *uint         // Pointer to allow null, for URLs that don't belong to a group
}

// URLGroup represents a collection of URLs extracted from a single source URL.
type URLGroup struct {
	gorm.Model
	Name      string       `gorm:"not null"`                                        // e.g., "Scripts from example.com"
	SourceURL string       `gorm:"unique;not null"`                                 // The URL used for extraction
	URLs      []WatchedUrl `gorm:"foreignKey:GroupID;constraint:OnDelete:CASCADE;"` // Add CASCADE constraint
}

// ChangeEvent represents a detected change for a WatchedUrl.
type ChangeEvent struct {
	gorm.Model           // Provides ID, CreatedAt, UpdatedAt, DeletedAt
	DiffText   string    `gorm:"not null"`
	DetectedAt time.Time `gorm:"not null"`
	URLID      uint      `gorm:"not null"`      // Foreign key to WatchedUrl
	IsRead     bool      `gorm:"default:false"` // Tracks if the change has been "read"
}
