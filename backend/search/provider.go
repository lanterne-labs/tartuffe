package search

// SearchResult represents a single item found by a search provider.
// It primarily contains the URL of the discovered content.
type SearchResult struct {
	// URL is the direct link to the found content.
	URL string
	// Title is the title of the search result, if available.
	Title string
	// Snippet is a brief description or snippet from the content, if available.
	Snippet string
}

// SearchProvider defines the interface for searching the web for content related to a person.
// Implementations could use various search engines or APIs.
type SearchProvider interface {
	// Search looks for recent online content mentioning the given personName.
	// It returns a slice of SearchResult structs or an error if the search fails.
	Search(personName string) ([]SearchResult, error)
} 