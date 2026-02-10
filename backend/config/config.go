package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all configuration for the application
type Config struct {
	Database   DatabaseConfig   `json:"database"`
	OpenAI     OpenAIConfig     `json:"openai"`
	Frpolitics FrpoliticsConfig `json:"frpolitics"`
}

// DatabaseConfig holds database configuration
type DatabaseConfig struct {
	URL string `json:"url"`
}

// OpenAIConfig holds OpenAI configuration
type OpenAIConfig struct {
	APIKey     string `json:"api_key"`
	BaseURL    string `json:"base_url"`
	ChatModel  string `json:"chat_model"`
	EmbedModel string `json:"embed_model"`
}

// FrpoliticsConfig holds frpolitics plugin configuration
type FrpoliticsConfig struct {
	Since          *time.Time `json:"since"`
	MaxLaws        int        `json:"max_laws"`
	MaxVotes       int        `json:"max_votes"`
	PartyThreshold float64    `json:"party_threshold"`
	Legislatures   []int      `json:"legislatures"`
	RevisitHours   int        `json:"revisit_hours"`
}

// LoadConfig loads configuration from .env file and environment variables
func LoadConfig() (*Config, error) {
	// Try to load .env file from project root
	if err := godotenv.Load(); err != nil {
		// Try from current directory
		if err := godotenv.Load(".env"); err != nil {
			// Try from parent directory
			if err := godotenv.Load("../.env"); err != nil {
				// Continue without .env file, rely on environment variables
			}
		}
	}

	config := &Config{}

	// Database configuration
	config.Database.URL = os.Getenv("DATABASE_URL")
	if config.Database.URL == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable is required")
	}

	// OpenAI configuration
	config.OpenAI.APIKey = os.Getenv("OPEN_AI_KEY")
	if config.OpenAI.APIKey == "" {
		return nil, fmt.Errorf("OPEN_AI_KEY environment variable is required")
	}

	config.OpenAI.BaseURL = getEnvWithDefault("OPEN_AI_API", "https://api.openai.com/v1")
	config.OpenAI.ChatModel = getEnvWithDefault("OPEN_AI_CHAT_MODEL", "gpt-3.5-turbo")
	config.OpenAI.EmbedModel = getEnvWithDefault("OPEN_AI_EMBED_MODEL", "text-embedding-ada-002")

	// Frpolitics configuration (with defaults)
	config.Frpolitics.MaxLaws = getEnvIntWithDefault("FRPOLITICS_MAX_LAWS", 50)
	config.Frpolitics.MaxVotes = getEnvIntWithDefault("FRPOLITICS_MAX_VOTES", 1000)
	config.Frpolitics.PartyThreshold = getEnvFloatWithDefault("FRPOLITICS_PARTY_THRESHOLD", 0.8)
	config.Frpolitics.RevisitHours = getEnvIntWithDefault("FRPOLITICS_REVISIT_HOURS", 24)

	// Parse legislatures
	if legsStr := os.Getenv("FRPOLITICS_LEGISLATURES"); legsStr != "" {
		config.Frpolitics.Legislatures = parseCommaSeparatedInts(legsStr)
	} else {
		config.Frpolitics.Legislatures = []int{16} // Default to 16th legislature
	}

	// Parse since date
	if sinceStr := os.Getenv("FRPOLITICS_SINCE"); sinceStr != "" {
		if since, err := time.Parse("2006-01-02", sinceStr); err == nil {
			config.Frpolitics.Since = &since
		}
	}

	return config, nil
}

// LoadConfigForTest loads configuration for testing, with optional .env file path
func LoadConfigForTest(envPath ...string) (*Config, error) {
	// Try to load .env file from specified path or defaults
	// Use Overload (not Load) so .env values always win over stale direnv/shell vars
	if len(envPath) > 0 && envPath[0] != "" {
		if err := godotenv.Overload(envPath[0]); err != nil {
			return nil, fmt.Errorf("failed to load .env file from %s: %w", envPath[0], err)
		}
	} else {
		// Try common test locations
		paths := []string{"../../../.env", "../../.env", "../.env", ".env"}
		for _, path := range paths {
			if err := godotenv.Overload(path); err == nil {
				break // Successfully loaded
			}
		}
	}

	return LoadConfig()
}

// getEnvWithDefault returns environment variable value or default
func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvIntWithDefault returns environment variable as int or default
func getEnvIntWithDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

// getEnvFloatWithDefault returns environment variable as float64 or default
func getEnvFloatWithDefault(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
	}
	return defaultValue
}

// parseCommaSeparatedInts parses comma-separated integers
func parseCommaSeparatedInts(s string) []int {
	var result []int
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			if intValue, err := strconv.Atoi(trimmed); err == nil {
				result = append(result, intValue)
			}
		}
	}
	return result
}
