package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"go-js-watcher/database"
	"go-js-watcher/handlers"
	"go-js-watcher/services"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type Template struct {
	templates *template.Template
}

func (t *Template) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

// humanReadableTime converts a time.Time to a human-readable string like "2 minutes ago".
// It handles nil pointers for optional times.
func humanReadableTime(t *time.Time) string {
	if t == nil {
		return "Never"
	}
	duration := time.Since(*t) // Calculate duration from now

	if duration.Minutes() < 1 {
		return "just now"
	} else if duration.Hours() < 1 {
		minutes := int(duration.Minutes())
		return fmt.Sprintf("%d minute%s ago", minutes, pluralS(minutes))
	} else if duration.Hours() < 24 {
		hours := int(duration.Hours())
		return fmt.Sprintf("%d hour%s ago", hours, pluralS(hours))
	} else if duration.Hours() < 24*7 { // Less than a week
		days := int(duration.Hours() / 24)
		return fmt.Sprintf("%d day%s ago", days, pluralS(days))
	} else if duration.Hours() < 24*30 { // Less than a month
		weeks := int(duration.Hours() / (24 * 7))
		return fmt.Sprintf("%d week%s ago", weeks, pluralS(weeks))
	} else if duration.Hours() < 24*365 { // Less than a year
		months := int(duration.Hours() / (24 * 30)) // Approximate month
		return fmt.Sprintf("%d month%s ago", months, pluralS(months))
	} else {
		years := int(duration.Hours() / (24 * 365)) // Approximate year
		return fmt.Sprintf("%d year%s ago", years, pluralS(years))
	}
}

// pluralS returns "s" if count is not 1, otherwise "".
func pluralS(count int) string {
	if count != 1 {
		return "s"
	}
	return ""
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found or error loading .env:", err)
	}

	// --- Configuration from Environment Variables ---
	flaskSecretKey := os.Getenv("FLASK_SECRET_KEY")
	if flaskSecretKey == "" {
		log.Println("FLASK_SECRET_KEY not set, using default. PLEASE SET A STRONG SECRET KEY IN PRODUCTION.")
		flaskSecretKey = "a-very-secret-key-default"
	}
	appUsername := os.Getenv("APP_USERNAME")
	if appUsername == "" {
		appUsername = "admin"
	}
	appPassword := os.Getenv("APP_PASSWORD")
	if appPassword == "" {
		appPassword = "password"
	}
	baseURL := os.Getenv("APP_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
		log.Println("APP_BASE_URL not set, defaulting to", baseURL)
	}

	telegramBotToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	telegramChatID := os.Getenv("TELEGRAM_CHAT_ID")

	handlers.AppUsername = appUsername
	handlers.AppPassword = appPassword
	handlers.BaseURL = baseURL
	handlers.SetSessionStore([]byte(flaskSecretKey))

	// --- Database Initialization ---
	database.Init()

	// --- Web Server Setup (Echo) ---
	e := echo.New()

	// e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	staticPath := filepath.Join(getExecutableDir(), "static")
	e.Static("/static", staticPath)

	// Register custom template functions here
	funcMap := template.FuncMap{
		"humanReadableTime": humanReadableTime, // Register our new function
	}

	templatesPath := filepath.Join(getExecutableDir(), "templates", "*.html")
	// Use Funcs() to add our custom functions before parsing templates
	tmpl := &Template{
		templates: template.Must(template.New("templates").Funcs(funcMap).ParseGlob(templatesPath)),
	}
	e.Renderer = tmpl

	// --- Routes ---
	e.GET("/login", handlers.LoginGet)
	e.POST("/login", handlers.LoginPost)
	e.GET("/logout", handlers.Logout)

	authGroup := e.Group("")
	authGroup.Use(handlers.AuthMiddleware)

	authGroup.GET("/", handlers.Dashboard)
	authGroup.GET("/dashboard", handlers.Dashboard)
	authGroup.POST("/add_url", func(c echo.Context) error {
		return handlers.AddURL(c, telegramBotToken, telegramChatID)
	})
	authGroup.POST("/remove_url", handlers.RemoveURL)
	authGroup.GET("/diff/:event_id", handlers.ViewDiff)

	// --- Start Background Scheduler ---
	services.StartScheduler(baseURL, telegramBotToken, telegramChatID)

	// --- Start the Web Server ---
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Starting web server on :%s", port)
	e.Logger.Fatal(e.Start(":" + port))
}

func getExecutableDir() string {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to get executable path: %v", err)
	}
	return filepath.Dir(exePath)
}
