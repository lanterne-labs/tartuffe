package cmd

import (
	// "fmt" // Unused
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	logLevel  string
	logFormat string // Flag variable, default set in init()
)

// Set the initial default logger to console format.
// This will be used by tests and as the default for the CLI unless overridden by flags.
func init() {
	// Configure the ConsoleWriter
	output := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.RFC3339,
		// Add FormatLevel to use standard uppercase level strings
		FormatLevel: func(i interface{}) string {
			level, ok := i.(string)
			if !ok {
				return strings.ToUpper(fmt.Sprintf("%s", i)) // Fallback
			}
			return strings.ToUpper(level)
		},
	}
	log.Logger = log.Output(output)

	// Set default flag values here
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Set log level (trace, debug, info, warn, error, fatal, panic)")
	// Default format is now console (set above), flag allows overriding to json
	rootCmd.PersistentFlags().StringVar(&logFormat, "log-format", "console", "Set log format (json, console)")

	// Other init tasks...
}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "digital-mirror", // Or choose a better name for your CLI
	Short: "A CLI tool for managing the digital mirror fact extraction pipeline.",
	Long: `Digital Mirror CLI provides tools to interact with the fact extraction 
pipeline, including running periodic jobs, manual testing, and database interactions.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Logger is already initialized to console by default in init().
		// Here we only handle potential overrides based on flags.

		// Set log level
		level, err := zerolog.ParseLevel(strings.ToLower(logLevel))
		if err != nil {
			level = zerolog.InfoLevel // Default to InfoLevel on parse error
			// Use the already configured logger for this warning
			log.Warn().Msgf("Invalid log level '%s', defaulting to 'info'", logLevel)
		}
		zerolog.SetGlobalLevel(level)

		// Check if JSON format is explicitly requested via the flag
		var logWriter io.Writer = nil // Use nil to signal no change unless JSON is requested
		if strings.ToLower(logFormat) == "json" {
			// Override the writer ONLY if json is requested
			logWriter = os.Stderr
			log.Info().Msg("Switching log format to JSON based on flag.")
		} else if strings.ToLower(logFormat) != "console" {
			// Warn if an invalid format is specified, but keep the console default
			log.Warn().Msgf("Invalid log format '%s', using default console format.", logFormat)
		}

		// Reconfigure global logger ONLY if the writer was changed (i.e., to JSON)
		if logWriter != nil {
			log.Logger = zerolog.New(logWriter).With().Timestamp().Logger()
		}

		// Log final effective settings (level is always set)
		finalFormat := "console" // Assume console unless changed
		if logWriter != nil {
			finalFormat = "json"
		}
		log.Info().Str("log-level", zerolog.GlobalLevel().String()).Str("log-format", finalFormat).Msg("Logger configured")
		return nil
	},
	// Uncomment the following line if your bare application
	// has an action associated with it:
	// Run: func(cmd *cobra.Command, args []string) { },
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		// Use zerolog for fatal error here too, though it might not be fully initialized if parsing flags failed
		// But cobra handles flag errors before PersistentPreRunE in many cases.
		log.Fatal().Err(err).Msg("Error executing command")
		os.Exit(1)
	}
}

func init() {
	// Add persistent flags for logging applicable to all commands
	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.digital-mirror.yaml)")

	// Cobra also supports local flags, which will only run
	// when this action is called directly.
	// rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
