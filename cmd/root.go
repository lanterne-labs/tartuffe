package cmd

import (
	// "fmt" // Unused
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
	logFormat string
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "digital-mirror", // Or choose a better name for your CLI
	Short: "A CLI tool for managing the digital mirror fact extraction pipeline.",
	Long: `Digital Mirror CLI provides tools to interact with the fact extraction 
pipeline, including running periodic jobs, manual testing, and database interactions.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Set log level
		level, err := zerolog.ParseLevel(strings.ToLower(logLevel))
		if err != nil {
			level = zerolog.InfoLevel // Default to InfoLevel on parse error
			log.Warn().Msgf("Invalid log level '%s', defaulting to 'info'", logLevel)
		}
		zerolog.SetGlobalLevel(level)

		// Set log format (JSON default, Console optional)
		var logWriter io.Writer = os.Stderr // Default: JSON to stderr
		if strings.ToLower(logFormat) == "console" {
			logWriter = zerolog.ConsoleWriter{
				Out:        os.Stderr,
				TimeFormat: time.RFC3339,
				// NoColor: false, // Keep colors by default for console
			}
		}

		// Configure global logger
		log.Logger = zerolog.New(logWriter).With().Timestamp().Logger()

		log.Info().Str("log-level", level.String()).Str("log-format", logFormat).Msg("Logger initialized")
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
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Set log level (trace, debug, info, warn, error, fatal, panic)")
	rootCmd.PersistentFlags().StringVar(&logFormat, "log-format", "console", "Set log format (json, console)")

	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.

	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.digital-mirror.yaml)")

	// Cobra also supports local flags, which will only run
	// when this action is called directly.
	// rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
} 