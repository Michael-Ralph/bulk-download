package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// tempFileStore holds references to generated ZIP files
// In a production app, you'd use a more robust storage solution
var (
	tempFileStore = make(map[string]string)
	storeMutex    = &sync.Mutex{}
)

func main() {
	// Initialize Echo instance
	e := echo.New()

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	// Static files
	e.Static("/static", "static")

	// Routes
	e.GET("/", serveIndex)
	e.POST("/compress", handleFileUpload)
	e.POST("/filename", handleFilename)
	e.GET("/download/:filename", handleDownload)

	// Start server
	e.Logger.Fatal(e.Start(":8080"))
}

// serveIndex renders our main HTML page
func serveIndex(c echo.Context) error {
	return c.File("templates/index.html")
}

// handleFilename returns the name of the selected file
func handleFilename(c echo.Context) error {
	file, err := c.FormFile("file")
	if err != nil {
		return c.HTML(http.StatusOK, "No file selected")
	}
	return c.HTML(http.StatusOK, file.Filename)
}

// handleDownload serves the ZIP file for download
func handleDownload(c echo.Context) error {
	filename := c.Param("filename")

	storeMutex.Lock()
	tempPath, exists := tempFileStore[filename]
	if !exists {
		storeMutex.Unlock()
		return c.HTML(http.StatusNotFound, "<div class='error'>File not found or expired</div>")
	}

	// Remove from the store immediately to prevent duplicate downloads
	delete(tempFileStore, filename)
	storeMutex.Unlock()

	// Open the file for reading
	file, err := os.Open(tempPath)
	if err != nil {
		return c.HTML(http.StatusInternalServerError, "<div class='error'>Error accessing file</div>")
	}

	// Schedule cleanup after download
	defer file.Close()
	defer os.Remove(tempPath)

	// Set headers for file download
	c.Response().Header().Set("Content-Type", "application/zip")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))

	// Stream the file to the client
	return c.Stream(http.StatusOK, "application/zip", file)
}

// handleFileUpload processes the uploaded file and returns a ZIP
func handleFileUpload(c echo.Context) error {
	// Get the file from the request
	file, err := c.FormFile("file")
	if err != nil {
		return c.HTML(http.StatusBadRequest, "<div class='error'>Error: No file uploaded</div>")
	}

	// Check file size (example: limit to 50MB)
	if file.Size > 50*1024*1024 {
		return c.HTML(http.StatusBadRequest, "<div class='error'>Error: File too large (max 50MB)</div>")
	}

	// Open the uploaded file
	src, err := file.Open()
	if err != nil {
		return c.HTML(http.StatusInternalServerError, "<div id='result'>Error opening uploaded file</div>")
	}
	defer src.Close()

	// Create a temporary file to store the ZIP
	tempFile, err := os.CreateTemp("", "archive-*.zip")
	if err != nil {
		return c.HTML(http.StatusInternalServerError, "<div class='error'>Error creating temporary file</div>")
	}
	// Don't remove the file immediately - it will be downloaded
	// We'll clean it up after download instead
	defer tempFile.Close()

	// Create a new ZIP archive
	zipWriter := zip.NewWriter(tempFile)
	defer zipWriter.Close()

	// Create a new file inside the ZIP archive
	zipFile, err := zipWriter.Create(file.Filename)
	if err != nil {
		return c.HTML(http.StatusInternalServerError, "<div class='error'>Error creating file in ZIP</div>")
	}

	// Copy the uploaded file data to the ZIP file
	if _, err := io.Copy(zipFile, src); err != nil {
		return c.HTML(http.StatusInternalServerError, "<div class='error'>Error copying file data</div>")
	}

	// Close the ZIP writer to finalize the archive
	zipWriter.Close()

	// Seek to the beginning of the temp file for reading
	tempFile.Seek(0, 0)

	// Generate a unique filename for the download
	zipFilename := fmt.Sprintf("%s_%s.zip",
		filepath.Base(file.Filename[:len(file.Filename)-len(filepath.Ext(file.Filename))]),
		time.Now().Format("20060102_150405"))

	// Set headers for file download
	c.Response().Header().Set("Content-Type", "application/zip")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", zipFilename))

	// For HTMX, we need to add a HX-Location header to redirect to download
	// This ensures the download still works without JavaScript
	downloadURL := fmt.Sprintf("/download/%s", zipFilename)

	// Store the temp file in a map or database for retrieval (simplified approach)
	// In production, use a more robust solution like session storage or a dedicated file service
	storeMutex.Lock()
	tempFileStore[zipFilename] = tempFile.Name()
	storeMutex.Unlock()

	// Return success message with download link
	successHTML := fmt.Sprintf(`
		<div class="success">
			File successfully compressed!
			<a href="%s" hx-boost="false">Download ZIP</a>
		</div>
	`, downloadURL)

	return c.HTML(http.StatusOK, successHTML)
}
