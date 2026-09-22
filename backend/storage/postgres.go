package storage

import (
	"context"

	"digital-mirror/backend/metrics"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"github.com/rs/zerolog/log"
)

// PostgresStorer implements the storage logic using PostgreSQL.
type PostgresStorer struct {
	pool *pgxpool.Pool
	// personGate is consulted before every NEW person INSERT (existing persons
	// are never re-checked). nil means every candidate is allowed. See person_gate.go.
	personGate PersonGate
	// historyCache memoizes GetPersonHistory per person; see person_history.go.
	historyCache historyCache
}

// NewPostgresStorerFromEnv creates a new PostgresStorer by connecting to the database
// specified by the DATABASE_URL environment variable.
func NewPostgresStorerFromEnv(ctx context.Context) (*PostgresStorer, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable not set")
	}

	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse DATABASE_URL: %w", err)
	}

	// Configure connection pool for long-running operations (like LLM calls)
	config.MaxConnLifetime = 30 * time.Minute   // Recycle connections periodically
	config.MaxConnIdleTime = 5 * time.Minute    // Close idle connections before server times them out
	config.HealthCheckPeriod = 30 * time.Second // Check connection health periodically
	config.MaxConns = 10                        // Reasonable pool size
	config.MinConns = 2                         // Keep some connections warm

	// Register pgvector types with the connection config
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		log.Debug().Msg("Registering pgvector types with pgx connection")
		return pgxvec.RegisterTypes(ctx, conn)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close() // Close pool if ping fails
		return nil, fmt.Errorf("unable to connect to database: %w", err)
	}

	log.Debug().Msg("Successfully connected to database with pgvector type registered.")

	return &PostgresStorer{pool: pool}, nil
}

// Close flushes what this process counted and closes the connection pool.
// Every process ends through here, so a CLI job's judgement counts reach the
// database even though only the web process is scraped.
func (s *PostgresStorer) Close() {
	if s.pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := metrics.FlushDecisions(ctx, s); err != nil {
		log.Warn().Err(err).Msg("Failed to flush decision counters on close")
	}
	log.Debug().Msg("Closing database connection pool.")
	s.pool.Close()
}

// Pool returns the underlying database connection pool.
// This is useful for operations outside the main StoreFact/Link methods,
// like the similarity check in the cron job.
func (s *PostgresStorer) Pool() *pgxpool.Pool {
	return s.pool
}

func scanInt64Set(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// StartMetricsCollectors runs the Prometheus gauge loops (pool statistics
// every 15 s, the "every fact has a stance" invariant every 5 min) until ctx
// is cancelled. Only the web process is scraped, so only it calls this; a
// collector in every CLI, cron and test process was wasted queries and, in
// tests, a deadlock source against TRUNCATE.
func (s *PostgresStorer) StartMetricsCollectors(ctx context.Context) {
	metrics.StartPoolStatsCollector(ctx, s.pool, 15*time.Second)
	metrics.StartStanceInvariantCollector(ctx, s.pool, 5*time.Minute)
	metrics.StartDecisionCollector(ctx, s, time.Minute)
	metrics.StartDecisionFlusher(ctx, s, time.Minute)
}
