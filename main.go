package main

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// tempFileStore holds references to generated ZIP files
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

	// Set up larger request size limit (100MB)
	e.Use(middleware.BodyLimit("100MB"))

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

// handleFilename returns the names of the selected files
func handleFilename(c echo.Context) error {
	// Get the form with multiple files
	form, err := c.MultipartForm()
	if err != nil {
		log.Printf("Error getting multipart form: %v", err)
		return c.HTML(http.StatusOK, "No files selected")
	}

	files, ok := form.File["files"]
	if !ok || len(files) == 0 {
		return c.HTML(http.StatusOK, "No files selected")
	}

	// Create an HTML list of selected files
	var fileListHTML string
	if len(files) == 1 {
		fileListHTML = files[0].Filename
	} else {
		fileListHTML = fmt.Sprintf("<strong>%d files selected:</strong><ul class='file-list'>", len(files))
		for i, file := range files {
			// Limit display to 5 files to avoid long lists
			if i >= 5 && len(files) > 6 {
				fileListHTML += fmt.Sprintf("<li>...and %d more</li>", len(files)-5)
				break
			}
			fileListHTML += fmt.Sprintf("<li>%s</li>", file.Filename)
		}
		fileListHTML += "</ul>"
	}

	return c.HTML(http.StatusOK, fileListHTML)
}

// handleFileUpload processes multiple uploaded files and returns a ZIP
func handleFileUpload(c echo.Context) error {
	// Get the form with multiple files
	form, err := c.MultipartForm()
	if err != nil {
		log.Printf("Error getting multipart form: %v", err)
		return c.HTML(http.StatusBadRequest, "<div class='error'>Error: Could not process form data</div>")
	}

	files, ok := form.File["files"]
	if !ok || len(files) == 0 {
		return c.HTML(http.StatusBadRequest, "<div class='error'>Error: No files selected</div>")
	}

	log.Printf("Processing %d files", len(files))

	// Check total size of all files (limit to 100MB total)
	var totalSize int64
	for _, file := range files {
		totalSize += file.Size
	}

	if totalSize > 100*1024*1024 {
		return c.HTML(http.StatusBadRequest, "<div class='error'>Error: Total file size too large (max 100MB)</div>")
	}

	// Create a temporary file to store the ZIP
	tempFile, err := os.CreateTemp("", "archive-*.zip")
	if err != nil {
		log.Printf("Error creating temp file: %v", err)
		return c.HTML(http.StatusInternalServerError, "<div class='error'>Error creating temporary file</div>")
	}
	defer tempFile.Close()

	// Create a new ZIP archive
	zipWriter := zip.NewWriter(tempFile)

	// Add each file to the ZIP archive
	for i, file := range files {
		log.Printf("Processing file %d: %s", i+1, file.Filename)

		// Open the current uploaded file
		src, err := file.Open()
		if err != nil {
			log.Printf("Error opening file %s: %v", file.Filename, err)
			zipWriter.Close() // Close the zip writer before returning
			return c.HTML(http.StatusInternalServerError,
				fmt.Sprintf("<div class='error'>Error opening file: %s</div>", file.Filename))
		}

		// Create a new file inside the ZIP archive
		zipFile, err := zipWriter.Create(file.Filename)
		if err != nil {
			log.Printf("Error creating zip entry for %s: %v", file.Filename, err)
			src.Close()
			zipWriter.Close() // Close the zip writer before returning
			return c.HTML(http.StatusInternalServerError,
				fmt.Sprintf("<div class='error'>Error adding %s to ZIP</div>", file.Filename))
		}

		// Copy the uploaded file data to the ZIP file
		if _, err := io.Copy(zipFile, src); err != nil {
			log.Printf("Error copying data for %s: %v", file.Filename, err)
			src.Close()
			zipWriter.Close() // Close the zip writer before returning
			return c.HTML(http.StatusInternalServerError,
				fmt.Sprintf("<div class='error'>Error copying %s data</div>", file.Filename))
		}

		src.Close() // Close the file after processing
	}

	// Close the ZIP writer to finalize the archive
	if err := zipWriter.Close(); err != nil {
		log.Printf("Error closing zip writer: %v", err)
		return c.HTML(http.StatusInternalServerError, "<div class='error'>Error finalizing ZIP archive</div>")
	}

	// Seek to the beginning of the temp file for later reading
	_, err = tempFile.Seek(0, 0)
	if err != nil {
		log.Printf("Error seeking temp file: %v", err)
		return c.HTML(http.StatusInternalServerError, "<div class='error'>Error preparing download</div>")
	}

	// Generate a unique filename for the download
	timestamp := time.Now().Format("20060102_150405")
	var baseFilename string
	if len(files) == 1 {
		fileName := files[0].Filename
		baseFilename = fileName[:len(fileName)-len(filepath.Ext(fileName))]
	} else {
		baseFilename = "archive"
	}

	zipFilename := fmt.Sprintf("%s_%s.zip", baseFilename, timestamp)
	tempFilePath := tempFile.Name()

	// Store the temp file path in map for retrieval
	storeMutex.Lock()
	tempFileStore[zipFilename] = tempFilePath
	storeMutex.Unlock()

	log.Printf("ZIP created successfully: %s (path: %s)", zipFilename, tempFilePath)

	// For HTMX, prepare download URL
	downloadURL := fmt.Sprintf("/download/%s", zipFilename)

	// Return success message with download link and file count
	var successMessage string
	if len(files) == 1 {
		successMessage = "File successfully compressed!"
	} else {
		successMessage = fmt.Sprintf("%d files successfully compressed!", len(files))
	}

	successHTML := fmt.Sprintf(`
		<div class="success">
			%s
			<a href="%s" class="download-link" hx-boost="false">Download ZIP</a>
		</div>
	`, successMessage, downloadURL)

	return c.HTML(http.StatusOK, successHTML)
}

// handleDownload serves the ZIP file for download
func handleDownload(c echo.Context) error {
	filename := c.Param("filename")

	log.Printf("Download requested for: %s", filename)

	storeMutex.Lock()
	tempPath, exists := tempFileStore[filename]
	if !exists {
		storeMutex.Unlock()
		log.Printf("File not found in store: %s", filename)
		return c.HTML(http.StatusNotFound, "<div class='error'>File not found or expired</div>")
	}

	// Remove from the store immediately to prevent duplicate downloads
	delete(tempFileStore, filename)
	storeMutex.Unlock()

	log.Printf("Serving file from: %s", tempPath)

	// Open the file for reading
	file, err := os.Open(tempPath)
	if err != nil {
		log.Printf("Error opening file for download: %v", err)
		return c.HTML(http.StatusInternalServerError, "<div class='error'>Error accessing file</div>")
	}

	// Schedule cleanup after download
	defer func() {
		file.Close()
		os.Remove(tempPath)
		log.Printf("Temp file removed: %s", tempPath)
	}()

	// Set headers for file download
	c.Response().Header().Set("Content-Type", "application/zip")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))

	// Stream the file to the client
	return c.Stream(http.StatusOK, "application/zip", file)
}
