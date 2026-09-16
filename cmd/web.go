package cmd

import (
	"context"
	"fmt"
	"os"

	"digital-mirror/backend/api"
	"digital-mirror/backend/deps"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	port string
)

// webCmd represents the web command group
var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Commands related to the web server.",
	Long:  `Start and manage the web server for the Digital Mirror API.`,
}

// serveCmd represents the serve command
var serveCmd = &cobra.Command{
	Use:         "serve",
	Short:       "Start the web server",
	Long:        `Start the HTTP server that serves the API endpoints and SSE streams.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        executeServe,
}

func init() {
	webCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(webCmd)

	defaultPort := "8080"
	if envPort := os.Getenv("PORT"); envPort != "" {
		defaultPort = envPort
	}
	serveCmd.Flags().StringVarP(&port, "port", "p", defaultPort, "Port to run the server on")
}

// executeServe is the main function for the serve command
func executeServe(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg("No .env file found, using environment variables")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	metricsPort := "9090"
	if v := os.Getenv("METRICS_PORT"); v != "" {
		metricsPort = v
	}

	return api.Serve(ctx, storer, api.Config{Port: port, MetricsPort: metricsPort})
}
