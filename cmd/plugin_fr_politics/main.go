package main

import (
	"context"
	"flag"
	"strconv"
	"strings"
	"time"

	fr "digital-mirror/backend/plugins/frpolitics"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

func main() {
	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("db init")
		return
	}
	defer storer.Close()

	// Flags (thin wrapper): --since, --from-year, --max-laws, --party-threshold, --legislatures, --revisit-hours
	sinceStr := flag.String("since", "", "process laws voted on or after YYYY-MM-DD")
	fromYear := flag.Int("from-year", 0, "derive legislatures from this start year (e.g., 2002)")
	maxLaws := flag.Int("max-laws", 50, "maximum laws to summarize per run")
	threshold := flag.Float64("party-threshold", 0.8, "majority threshold for party-level facts (0..1)")
	legsStr := flag.String("legislatures", "16", "comma-separated legislature numbers to fetch (e.g., 16,17)")
	revisit := flag.Int("revisit-hours", 0, "revisit window in hours for source URLs (0=always revisit now)")
	flag.Parse()

	var since *time.Time
	if *sinceStr != "" {
		if t, err := time.Parse("2006-01-02", *sinceStr); err == nil {
			since = &t
		} else {
			log.Warn().Str("since", *sinceStr).Msg("invalid --since, expected YYYY-MM-DD; ignoring")
		}
	}

	// Parse legislatures
	var legs []int
	if *legsStr != "" {
		for _, p := range strings.Split(*legsStr, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if n, err := strconv.Atoi(p); err == nil {
				legs = append(legs, n)
			} else {
				log.Warn().Str("leg", p).Msg("invalid legislature; ignoring")
			}
		}
	}
	// If from-year is set and --since not provided, set since to Jan 1 of that year
	if since == nil && *fromYear > 0 {
		t := time.Date(*fromYear, time.January, 1, 0, 0, 0, 0, time.UTC)
		since = &t
	}
	opts := fr.Options{Since: since, MaxLaws: *maxLaws, PartyThreshold: *threshold, Legislatures: legs, RevisitHours: *revisit}
	if err := fr.Run(ctx, storer, opts); err != nil {
		log.Fatal().Err(err).Msg("frpolitics run failed")
	}
}
