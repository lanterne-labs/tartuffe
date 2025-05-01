package util

// ... existing code ... (assuming NormalizeString is here)

// TruncateString limits a string to a maximum length, adding ellipsis if truncated.
func TruncateString(s string, maxLength int) string {
	if len(s) <= maxLength {
		return s
	}
	if maxLength <= 3 {
		return "..."
	}
	return s[:maxLength-3] + "..."
}
