package cmd

import (
	"fmt"
	"unicode/utf8"
)

// ANSI colour codes for console output.
const (
	ansiReset  = "\x1b[0m"
	ansiGreen  = "\x1b[32m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiGray   = "\x1b[90m"
)

// resultRow is one "label: value" line of a printResults block.
type resultRow struct {
	Label string
	Value int64
	Note  string // trailing annotation, printed after the value
}

// printResults prints a "=== title ===" block with one counter per row, the
// values aligned on the longest label.
func printResults(title string, rows []resultRow) {
	width := 0
	for _, r := range rows {
		if n := utf8.RuneCountInString(r.Label); n > width {
			width = n
		}
	}
	fmt.Println()
	fmt.Printf("=== %s ===\n", title)
	for _, r := range rows {
		fmt.Printf("%-*s %d", width+1, r.Label+":", r.Value)
		if r.Note != "" {
			fmt.Printf(" %s", r.Note)
		}
		fmt.Println()
	}
}
