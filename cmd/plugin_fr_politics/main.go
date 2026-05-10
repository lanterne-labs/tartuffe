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

	// Flags (thin wrapper): --since, --last-years, --from-year, --max-laws, --party-threshold, --legislatures, --revisit-hours
	sinceStr := flag.String("since", "", "process laws voted on or after YYYY-MM-DD (auto-derives legislatures)")
	lastYears := flag.Int("last-years", 0, "process votes from the last N years, e.g. --last-years 2 (auto-derives since and legislatures)")
	fromYear := flag.Int("from-year", 0, "process votes starting from Jan 1 of this year, e.g. --from-year 2022 (auto-derives legislatures)")
	maxLaws := flag.Int("max-laws", 50, "maximum laws to summarize per run")
	threshold := flag.Float64("party-threshold", 0.8, "majority threshold for party-level facts (0..1)")
	legsStr := flag.String("legislatures", "", "comma-separated legislature numbers to fetch (default: auto-derive from date range or current)")
	revisit := flag.Int("revisit-hours", 0, "revisit window in hours for source URLs (0=always revisit now)")
	flag.Parse()

	var since *time.Time
	if *sinceStr != "" {
		if t, err := time.Parse("2006-01-02", *sinceStr); err == nil {
			since = &t
		} else {
			log.Warn().Str("since", *sinceStr).Msg("invalid --since, expected YYYY-MM-DD; ignoring")
		}
	} else if *lastYears > 0 {
		t := time.Now().AddDate(-*lastYears, 0, 0)
		since = &t
		log.Info().Int("last_years", *lastYears).Str("since", t.Format("2006-01-02")).Msg("derived --since from --last-years")
	} else if *fromYear > 0 {
		t := time.Date(*fromYear, time.January, 1, 0, 0, 0, 0, time.UTC)
		since = &t
		log.Info().Int("from_year", *fromYear).Str("since", t.Format("2006-01-02")).Msg("derived --since from --from-year")
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
	opts := fr.Options{Since: since, MaxLaws: *maxLaws, PartyThreshold: *threshold, Legislatures: legs, RevisitHours: *revisit}
	if err := fr.Run(ctx, storer, opts); err != nil {
		log.Fatal().Err(err).Msg("frpolitics run failed")
	}
}
