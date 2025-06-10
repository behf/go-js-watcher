package main

import (
	"html/template"
	"io"
	"log"
	"os"
	"path/filepath"

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

	// Load Telegram credentials AFTER godotenv.Load()
	telegramBotToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	telegramChatID := os.Getenv("TELEGRAM_CHAT_ID")

	// Set handlers package variables
	handlers.AppUsername = appUsername
	handlers.AppPassword = appPassword
	handlers.BaseURL = baseURL
	handlers.SetSessionStore([]byte(flaskSecretKey))

	// --- Database Initialization ---
	database.Init()

	// --- Web Server Setup (Echo) ---
	e := echo.New()

	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	staticPath := filepath.Join(getExecutableDir(), "static")
	e.Static("/static", staticPath)

	templatesPath := filepath.Join(getExecutableDir(), "templates", "*.html")
	tmpl := &Template{
		templates: template.Must(template.ParseGlob(templatesPath)),
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
		// Pass credentials to AddURL (which then calls CheckURLForChanges)
		return handlers.AddURL(c, telegramBotToken, telegramChatID)
	})
	authGroup.POST("/remove_url", handlers.RemoveURL)
	authGroup.GET("/diff/:event_id", handlers.ViewDiff)

	// --- Start Background Scheduler ---
	// Pass credentials to StartScheduler
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
