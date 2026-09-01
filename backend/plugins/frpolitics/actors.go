package frpolitics

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/rs/zerolog/log"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Note: getCacheDir() is defined in download.go

// amo30URL is the AN open-data archive that carries every actor, mandate AND
// organe (historical, all legislatures). The per-legislature
// repository/<n>/organes/Organes.json.zip path it replaced now returns 404.
const amo30URL = "https://data.assemblee-nationale.fr/static/openData/repository/17/amo/tous_acteurs_mandats_organes_xi_legislature/AMO30_tous_acteurs_tous_mandats_tous_organes_historique.json.zip"

// loadAMO30Archive returns the AMO30 zip bytes: a local copy stored as
// Organes.json.zip in the cache dir wins, else the archive is downloaded
// through the 7-day cache. It is the single source for both actor names and
// organe names, so a failure here is an error, not an empty result.
func loadAMO30Archive(ctx context.Context) ([]byte, error) {
	local := filepath.Join(getCacheDir(), "Organes.json.zip")
	if b, err := os.ReadFile(local); err == nil && len(b) > 0 {
		return b, nil
	}
	b, err := downloadCached(ctx, amo30URL, getCacheDir(), 7*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("download AMO30 archive: %w", err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("AMO30 archive at %s is empty", amo30URL)
	}
	return b, nil
}

// loadActorNameMap builds a map from AssembleeID (e.g., "PA1234") to display name "First Last".
// Best-effort: deputies whose name is missing are resolved later from the vote data.
func loadActorNameMap(ctx context.Context) map[string]string {
	out := make(map[string]string)
	blob, err := loadAMO30Archive(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("frpolitics: actor names unavailable")
		return out
	}

	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		// Fallback: treat as raw JSON/NDJSON buffer
		parseActorBuffer(out, blob)
		return out
	}

	for _, f := range zr.File {
		// Only JSON entries under acteur/ directory
		if !strings.HasSuffix(strings.ToLower(f.Name), ".json") {
			continue
		}
		if !strings.Contains(f.Name, "acteur") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		// Allow up to ~50MB per entry to accommodate large bundles
		data, err := io.ReadAll(io.LimitReader(rc, 50<<20))
		rc.Close()
		if err != nil {
			continue
		}
		// Parse and extract actor ID and name
		extractActor(out, data)
	}
	return out
}

// loadActorNameMapFromPath reads an actors/AMO zip strictly from a given local path.
// It does not attempt any network download; returns an empty map on error.
func loadActorNameMapFromPath(ctx context.Context, zipPath string) (map[string]string, error) {
	if _, err := os.Stat(zipPath); err != nil {
		return map[string]string{}, err
	}
	b, err := os.ReadFile(zipPath)
	if err != nil {
		return map[string]string{}, err
	}
	out := make(map[string]string)
	// Try as zip; if not a zip, parse the buffer directly (JSON/NDJSON)
	if zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b))); err == nil {
		for _, f := range zr.File {
			if !strings.HasSuffix(strings.ToLower(f.Name), ".json") {
				continue
			}
			// Only process acteur entries
			if !strings.Contains(f.Name, "acteur") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(rc, 50<<20))
			rc.Close()
			if err != nil {
				continue
			}
			extractActor(out, data)
		}
		return out, nil
	}
	parseActorBuffer(out, b)
	return out, nil
}

// uidField handles the uid field which can be either a string or an object with #text.
type uidField struct {
	Text string `json:"#text"`
}

// UnmarshalJSON handles both string and object forms of uid.
func (u *uidField) UnmarshalJSON(data []byte) error {
	// Try as plain string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		u.Text = s
		return nil
	}
	// Try as object with #text
	type obj struct {
		Text string `json:"#text"`
	}
	var o obj
	if err := json.Unmarshal(data, &o); err == nil {
		u.Text = o.Text
		return nil
	}
	return nil
}

// acteurRecord represents a single actor entry in the JSON.
type acteurRecord struct {
	Acteur struct {
		Uid       uidField `json:"uid"`
		EtatCivil struct {
			Ident struct {
				Nom         string `json:"nom"`
				NomUsuel    string `json:"nomUsuel"`
				Prenom      string `json:"prenom"`
				PrenomUsuel string `json:"prenomUsuel"`
			} `json:"ident"`
		} `json:"etatCivil"`
	} `json:"acteur"`
}

// extractActor parses a single actor JSON entry and adds it to the map.
func extractActor(out map[string]string, data []byte) {
	var rec acteurRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return
	}
	ref := strings.TrimSpace(rec.Acteur.Uid.Text)
	if ref == "" {
		return
	}
	nom := firstNonEmpty(rec.Acteur.EtatCivil.Ident.NomUsuel, rec.Acteur.EtatCivil.Ident.Nom)
	prenom := firstNonEmpty(rec.Acteur.EtatCivil.Ident.PrenomUsuel, rec.Acteur.EtatCivil.Ident.Prenom)
	display := strings.TrimSpace(strings.TrimSpace(prenom) + " " + strings.TrimSpace(nom))
	if display != "" {
		out[ref] = display
	}
}

// parseActorBuffer appends actor entries decoded from a generic JSON/NDJSON buffer.
func parseActorBuffer(out map[string]string, data []byte) {
	// Try single record
	var rec acteurRecord
	if json.Unmarshal(data, &rec) == nil && strings.TrimSpace(rec.Acteur.Uid.Text) != "" {
		extractActor(out, data)
		return
	}
	// Try array
	var arr []acteurRecord
	if json.Unmarshal(data, &arr) == nil {
		for _, a := range arr {
			ref := strings.TrimSpace(a.Acteur.Uid.Text)
			if ref == "" {
				continue
			}
			nom := firstNonEmpty(a.Acteur.EtatCivil.Ident.NomUsuel, a.Acteur.EtatCivil.Ident.Nom)
			prenom := firstNonEmpty(a.Acteur.EtatCivil.Ident.PrenomUsuel, a.Acteur.EtatCivil.Ident.Prenom)
			display := strings.TrimSpace(strings.TrimSpace(prenom) + " " + strings.TrimSpace(nom))
			if display != "" {
				out[ref] = display
			}
		}
		return
	}
	// Try NDJSON
	sc := bufio.NewScanner(bytes.NewReader(data))
	const maxLine = 1024 * 1024
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, maxLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		extractActor(out, line)
	}
}
