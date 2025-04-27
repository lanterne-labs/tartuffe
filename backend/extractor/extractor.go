package extractor

import "time"

// ExtractedContent holds the structured data retrieved from a web page.
type ExtractedContent struct {
	// URL is the original URL from which the content was extracted.
	URL string `json:"url"`
	// Title is the title of the web page.
	Title string `json:"title"`
	// DatePublished is the publication date found on the page, if available.
	DatePublished *time.Time `json:"date_published,omitempty"` // Use pointer for optional field
	// Content is the main textual content extracted from the page.
	Content string `json:"content"`
	// RawHTML is the raw HTML source, optional, might be useful for debugging.
	RawHTML string `json:"raw_html,omitempty"` // Optional raw HTML
	// Add other metadata fields as needed from the scraper (e.g., Author)
}

// ContentExtractor defines the interface for extracting structured content from a given URL.
// Implementations might use scraping APIs (like Apify) or local scraping libraries.
type ContentExtractor interface {
	// ExtractContent fetches the content from the given URL and returns it in a structured format.
	// It returns an ExtractedContent struct or an error if extraction fails.
	ExtractContent(url string) (*ExtractedContent, error)
} 