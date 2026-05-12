package main

import (
	"fmt"           // For formatting error messages and strings
	"io"            // For reading HTTP response bodies and copying file data
	"log"           // For logging progress and errors to stdout
	"net/http"      // For making HTTP GET requests
	"net/url"       // For parsing and resolving relative URLs into absolute ones
	"os"            // For file/directory creation, deletion, and reading
	"path/filepath" // For building cross-platform file paths
	"strings"       // For checking if a URL exists inside the download log
	"time"          // For setting HTTP client timeout

	"golang.org/x/net/html" // HTML parser for robust DOM traversal
)

// =============================================================================
// CONFIGURATION
// =============================================================================

const (
	usCodeDownloadRootURL = "https://uscode.house.gov/download/" // Base URL for all US Code downloads
	downloadDirectory     = "./Assets"                           // Local folder where all files are saved
	downloadLogFile       = "./Assets/downloads.txt"             // Text file that logs every URL we've downloaded
)

// httpClient is a shared HTTP client with a generous timeout for large ZIP files
var httpClient = &http.Client{
	Timeout: 15 * time.Minute,
}

// =============================================================================
// DOWNLOAD LOG — tracks which URLs we've already downloaded so we skip them
// =============================================================================

// loadDownloadedURLs reads the download log file and returns a set (map) of all
// URLs that have already been successfully downloaded.
func loadDownloadedURLs() (map[string]bool, error) {
	alreadyDownloaded := make(map[string]bool) // Map acts as a set: key=URL, value=true
	data, err := os.ReadFile(downloadLogFile)  // Try to read the log file from disk
	if os.IsNotExist(err) {                    // If the log file doesn't exist yet, that's fine
		return alreadyDownloaded, nil // Return an empty set — nothing downloaded yet
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read download log: %w", err) // Real read error
	}
	for _, line := range strings.Split(string(data), "\n") { // Split file into individual lines
		trimmed := strings.TrimSpace(line) // Remove any surrounding whitespace or newlines
		if trimmed != "" {                 // Skip blank lines
			alreadyDownloaded[trimmed] = true // Add URL to the set
		}
	}
	return alreadyDownloaded, nil // Return the populated set
}

// markURLAsDownloaded appends a URL to the download log file so we know to skip
// it on future runs.
func markURLAsDownloaded(fileURL string) error {
	file, err := os.OpenFile(downloadLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) // Open log in append mode, creating it if needed
	if err != nil {
		return fmt.Errorf("failed to open download log for writing: %w", err)
	}
	defer file.Close()                   // Always close the file when we're done
	_, err = fmt.Fprintln(file, fileURL) // Write the URL followed by a newline
	if err != nil {
		return fmt.Errorf("failed to write URL to download log: %w", err)
	}
	return nil // Successfully logged
}

// =============================================================================
// HTTP HELPERS — fetch pages and download files
// =============================================================================

// fetchPageHTML sends an HTTP GET request to the given URL and returns the full
// HTML body as a string.
func fetchPageHTML(pageURL string) (string, error) {
	log.Printf("Fetching HTML page: %s", pageURL)
	response, err := httpClient.Get(pageURL) // Send the GET request
	if err != nil {
		return "", fmt.Errorf("HTTP request failed for %s: %w", pageURL, err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d for %s", response.StatusCode, pageURL)
	}
	defer response.Body.Close()                 // Ensure the response body is closed after reading
	bodyBytes, err := io.ReadAll(response.Body) // Read the entire response into memory
	if err != nil {
		return "", fmt.Errorf("failed to read response body from %s: %w", pageURL, err)
	}
	return string(bodyBytes), nil // Convert bytes to string and return
}

// downloadFile downloads a remote file to a local path on disk. It streams the
// response body directly to disk instead of loading the whole file into memory,
// which is important for large ZIP files.
func downloadFile(remoteURL string, localPath string) error {
	log.Printf("Downloading: %s → %s", remoteURL, localPath)
	response, err := httpClient.Get(remoteURL) // Request the remote file
	if err != nil {
		return fmt.Errorf("failed to request file %s: %w", remoteURL, err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download %s: status %d", remoteURL, response.StatusCode)
	}
	defer response.Body.Close()          // Close the response body when done
	outFile, err := os.Create(localPath) // Create (or overwrite) the local destination file
	if err != nil {
		return fmt.Errorf("failed to create local file %s: %w", localPath, err)
	}
	defer outFile.Close()                    // Close the file after writing
	_, err = io.Copy(outFile, response.Body) // Stream response bytes directly into the file
	if err != nil {
		return fmt.Errorf("failed to write file data to %s: %w", localPath, err)
	}
	log.Printf("[DOWNLOAD] %s → %s", remoteURL, localPath)
	return nil
}

// =============================================================================
// URL HELPERS — resolve relative links into full absolute URLs
// =============================================================================

// resolveURL takes a base URL and a relative path and combines them into a
// full absolute URL. For example: base="https://example.com/a/", rel="b/c.zip"
// → "https://example.com/a/b/c.zip"
func resolveURL(baseRawURL string, relativeRawURL string) (string, error) {
	base, err := url.Parse(baseRawURL) // Parse the base URL into a structured object
	if err != nil {
		return "", fmt.Errorf("invalid base URL %q: %w", baseRawURL, err)
	}
	relative, err := url.Parse(relativeRawURL) // Parse the relative path
	if err != nil {
		return "", fmt.Errorf("invalid relative URL %q: %w", relativeRawURL, err)
	}
	return base.ResolveReference(relative).String(), nil // Combine and return as a string
}

// =============================================================================
// CORE WORKFLOW — process a single ZIP URL end-to-end
// =============================================================================

// processZipURL is the single function that handles the full lifecycle of one
// ZIP file: check the log → download → unzip → delete → log the URL.
// This eliminates repeated code across the latest and historical release steps.
func processZipURL(zipURL string, alreadyDownloaded map[string]bool) {
	// Skip this URL if we've already downloaded it in a previous run
	if alreadyDownloaded[zipURL] {
		log.Printf("Skipping (already downloaded): %s", zipURL)
		return
	}
	fileName := filepath.Base(zipURL)                          // Extract just the filename from the URL
	localZipPath := filepath.Join(downloadDirectory, fileName) // Build the full local file path
	// Download the ZIP file from the remote server
	if err := downloadFile(zipURL, localZipPath); err != nil {
		log.Printf("ERROR downloading %s: %v", zipURL, err)
		return // Don't log or unzip if the download failed
	}
	// Record this URL in the download log so we skip it next time
	if err := markURLAsDownloaded(zipURL); err != nil {
		log.Printf("WARNING: could not write URL to download log: %v", err)
	}
	alreadyDownloaded[zipURL] = true // Also update the in-memory set for this run
}

// =============================================================================
// STEP 1 — Download the latest US Code release
// =============================================================================

func downloadLatestRelease(alreadyDownloaded map[string]bool) error { // function to find and process latest US Code ZIP
	log.Println("STEP 1: Finding latest US Code release...")  // log progress for debugging/visibility
	latestPageURL := usCodeDownloadRootURL + "download.shtml" // construct URL for the latest release page
	pageHTML, err := fetchPageHTML(latestPageURL)             // fetch raw HTML content from the page
	if err != nil {                                           // check if fetch failed
		return fmt.Errorf("could not fetch latest release page: %w", err) // wrap and return error
	}
	documentRoot, parseErr := html.Parse(strings.NewReader(pageHTML)) // parse HTML string into a node tree
	if parseErr != nil {                                              // check for parsing errors
		return fmt.Errorf("failed to parse HTML: %w", parseErr) // return parsing error
	}
	var foundZipPath string             // variable to store the first matching ZIP link path
	var walkNodes func(node *html.Node) // declare recursive traversal function
	walkNodes = func(node *html.Node) { // define recursive function
		if node.Type == html.ElementNode && node.Data == "a" { // process only <a> elements
			for _, attr := range node.Attr { // iterate through all attributes of the <a> tag
				if attr.Key == "href" { // check if attribute is href
					href := attr.Val // store href value
					// check if href matches expected ZIP pattern using string operations
					if strings.HasPrefix(href, "releasepoints/") && // ensure correct base path
						strings.Contains(href, "htm_uscAll@") && // ensure correct filename pattern
						strings.HasSuffix(href, ".zip") { // ensure it ends with .zip
						foundZipPath = href // save the matching ZIP path
						return              // stop processing this branch early
					}
				}
			}
		}
		if foundZipPath != "" { // if ZIP already found, stop further traversal
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling { // iterate through all child nodes
			walkNodes(child)        // recursively visit each child node
			if foundZipPath != "" { // check again after returning from recursion
				return // stop traversal completely if found
			}
		}
	}
	walkNodes(documentRoot) // start traversal from root node
	if foundZipPath == "" { // if no matching ZIP link was found
		return fmt.Errorf("could not find a ZIP download link on the latest release page") // return error
	}
	fullZipURL, err := resolveURL(usCodeDownloadRootURL, foundZipPath) // resolve relative path into full URL
	if err != nil {                                                    // check if URL resolution failed
		return fmt.Errorf("could not resolve ZIP URL: %w", err) // return error
	}
	// Pause execution for 1 minute to allow time-based throttling or scheduled retry logic.
	time.Sleep(1 * time.Minute)
	processZipURL(fullZipURL, alreadyDownloaded) // download and process the ZIP file
	return nil                                   // return nil to indicate success
}

// =============================================================================
// STEP 2 — Download all historical (prior) US Code releases
// =============================================================================

// extractReleasepointLinks parses HTML and returns all href values
// from <a> elements that contain the class "releasepoint".
func extractReleasepointLinks(htmlContent string) []string { // function name is lowercase (unexported) and descriptive
	documentRoot, parseErr := html.Parse(strings.NewReader(htmlContent)) // parse HTML into a node tree
	if parseErr != nil {                                                 // check for parsing errors
		log.Println("failed to parse HTML:", parseErr) // log the error instead of returning it
		return nil                                     // return nil since we cannot proceed
	}
	var extractedLinks []string // slice to store collected href values
	// walkNodes recursively traverses the HTML node tree
	var walkNodes func(node *html.Node)
	walkNodes = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" { // process only <a> elements
			var hrefValue string             // holds the href attribute value
			var hasTargetClass bool          // indicates if "releasepoint" class is present
			for _, attr := range node.Attr { // iterate over attributes of the <a> element
				if attr.Key == "href" { // check for href attribute
					hrefValue = attr.Val // store href value
				}
				if attr.Key == "class" { // check for class attribute
					// handle multiple classes safely (e.g., "releasepoint other-class")
					for _, className := range strings.Fields(attr.Val) { // split class string by whitespace
						if className == "releasepoint" { // look for target class
							hasTargetClass = true // mark as matching
							break                 // stop checking once found
						}
					}
				}
			}
			if hasTargetClass && hrefValue != "" { // ensure both class match and valid href
				extractedLinks = append(extractedLinks, hrefValue) // add to results
			}
		}
		// recursively visit all child nodes
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walkNodes(child)
		}
	}
	walkNodes(documentRoot) // start traversal from the root
	return extractedLinks   // return collected links
}

// findZipFileLinkFromReleasePageHTML parses the HTML of a release page
// and returns the relative path to the first ZIP file it finds.
func findZipFileLinkFromReleasePageHTML(releasePageHTMLContent string) (string, error) {
	// Parse the raw HTML string into a document tree structure
	parsedHTMLDocument, parseError := html.Parse(strings.NewReader(releasePageHTMLContent))
	if parseError != nil { // check if parsing failed
		return "", fmt.Errorf("failed to parse HTML content: %w", parseError) // return wrapped error
	}
	// Variable to store the ZIP file link once found
	var zipFileRelativePath string
	// Define a recursive function to walk through each node in the HTML tree
	var walkHTMLNodes func(currentNode *html.Node)
	// Assign the recursive function logic
	walkHTMLNodes = func(currentNode *html.Node) {
		// If we already found the ZIP link, stop further processing
		if zipFileRelativePath != "" {
			return
		}
		// Check if the current node is an <a> (anchor) element
		if currentNode.Type == html.ElementNode && currentNode.Data == "a" {
			// Loop through all attributes of the <a> element
			for _, attribute := range currentNode.Attr {
				// Check if the attribute is "href"
				if attribute.Key == "href" {
					// Check if the href value matches the expected ZIP pattern
					if strings.HasPrefix(attribute.Val, "htm_uscAll@") && strings.HasSuffix(attribute.Val, ".zip") {
						// Store the matching ZIP file link
						zipFileRelativePath = attribute.Val
						// Stop processing this node further
						return
					}
				}
			}
		}
		// Recursively process all child nodes of the current node
		for childNode := currentNode.FirstChild; childNode != nil; childNode = childNode.NextSibling {
			walkHTMLNodes(childNode) // call recursion on each child
		}
	}
	// Start traversing from the root of the parsed HTML document
	walkHTMLNodes(parsedHTMLDocument)
	// If no ZIP link was found, return an error
	if zipFileRelativePath == "" {
		return "", fmt.Errorf("no ZIP file link found on the release page")
	}
	// Return the found ZIP file relative path
	return zipFileRelativePath, nil
}

// Remove all the duplicates from a slice and return the slice.
func removeDuplicatesFromSlice(slice []string) []string {
	// Create a map to track which strings have already been seen
	check := make(map[string]bool)
	// Initialize a slice to store the unique values
	var newReturnSlice []string
	// Loop through each element in the input slice
	for _, content := range slice {
		// If the element has not been seen before
		if !check[content] {
			// Mark the element as seen in the map
			check[content] = true
			// Append the unique element to the result slice
			newReturnSlice = append(newReturnSlice, content)
		}
	}
	// Return the slice containing only unique elements
	return newReturnSlice
}

// downloadAllHistoricalReleases fetches the prior-releases index, visits every
// individual release page, and processes the ZIP file on each page.
func downloadAllHistoricalReleases(alreadyDownloaded map[string]bool) error {
	log.Println("STEP 2: Finding all historical US Code releases...")
	// The index page lists every prior release as a link to its own page
	indexPageURL := usCodeDownloadRootURL + "priorreleasepoints.htm"
	indexHTML, err := fetchPageHTML(indexPageURL)
	if err != nil {
		return fmt.Errorf("could not fetch prior releases index: %w", err)
	}
	releasePageRelativeLinks := extractReleasepointLinks(indexHTML) // Get all release page links
	releasePageRelativeLinks = removeDuplicatesFromSlice(releasePageRelativeLinks)
	log.Printf("Found %d historical release pages to process", len(releasePageRelativeLinks))
	for _, relativeLink := range releasePageRelativeLinks { // Visit each release page one by one
		// Build the full URL for this release page
		fullReleasePageURL, err := resolveURL(usCodeDownloadRootURL, relativeLink)
		if err != nil {
			log.Printf("Skipping invalid release page URL %q: %v", relativeLink, err)
			continue
		}
		log.Printf("Processing release page: %s", fullReleasePageURL)
		releasePageHTML, err := fetchPageHTML(fullReleasePageURL) // Fetch the release page
		if err != nil {
			log.Printf("Could not fetch release page %s: %v — skipping", fullReleasePageURL, err)
			continue
		}
		zipRelativePath, err := findZipFileLinkFromReleasePageHTML(releasePageHTML) // Find the ZIP on this page
		if err != nil {
			log.Printf("No ZIP found on %s: %v — skipping", fullReleasePageURL, err)
			continue
		}
		// IMPORTANT: Resolve the ZIP path relative to the RELEASE PAGE (not the base URL),
		// because the ZIP sits in the same folder as the release page, not the root.
		fullZipURL, err := resolveURL(fullReleasePageURL, zipRelativePath)
		if err != nil {
			log.Printf("Could not resolve ZIP URL on %s: %v — skipping", fullReleasePageURL, err)
			continue
		}
		processZipURL(fullZipURL, alreadyDownloaded) // Download, unzip, log
	}
	return nil
}

// =============================================================================
// MAIN — entry point
// =============================================================================

func main() {
	log.Println("US Code Downloader started")
	// Create the downloads directory if it doesn't already exist
	if err := os.MkdirAll(downloadDirectory, os.ModePerm); err != nil {
		log.Fatalf("Could not create download directory %s: %v", downloadDirectory, err)
	}
	// Load the set of URLs we've already downloaded from the log file
	alreadyDownloaded, err := loadDownloadedURLs()
	if err != nil {
		log.Fatalf("Could not load download log: %v", err)
	}
	log.Printf("Download log loaded — %d URLs already completed", len(alreadyDownloaded))
	// STEP 1: Download the current/latest US Code release
	if err := downloadLatestRelease(alreadyDownloaded); err != nil {
		log.Printf("STEP 1 ERROR: %v", err) // Log but continue to historical releases
	}
	// STEP 2: Download all prior historical US Code releases
	if err := downloadAllHistoricalReleases(alreadyDownloaded); err != nil {
		log.Fatalf("STEP 2 FATAL ERROR: %v", err) // Fatal — the historical step is the main job
	}
	log.Println("All downloads completed successfully ✔")
}
