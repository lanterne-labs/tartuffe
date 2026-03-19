package frpolitics

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Note: getCacheDir() is defined in download.go

// loadActorNameMap builds a map from AssembleeID (e.g., "PA1234") to display name "First Last".
// It tries multiple known locations and returns a best-effort result.
func loadActorNameMap(ctx context.Context) map[string]string {
	out := make(map[string]string)

	// Try local cached file first (if present) - user stores AMO30 (historical) as Organes.json.zip
	tryLocal := func() []byte {
		cacheDir := getCacheDir()
		local := filepath.Join(cacheDir, "Organes.json.zip")
		if _, err := os.Stat(local); err == nil {
			if b, err := os.ReadFile(local); err == nil {
				return b
			}
		}
		return nil
	}

	// Attempt downloads from official historical dataset (AMO30) for legislature 17 (XI legislature historical)
	sources := []string{
		"https://data.assemblee-nationale.fr/static/openData/repository/17/amo/tous_acteurs_mandats_organes_xi_legislature/AMO30_tous_acteurs_tous_mandats_tous_organes_historique.json.zip",
	}

	var blob []byte
	blob = tryLocal()
	if blob == nil {
		for _, u := range sources {
			b, err := downloadCached(ctx, u, getCacheDir(), 7*24*time.Hour)
			if err == nil && len(b) > 0 {
				blob = b
				break
			}
		}
	}
	if blob == nil {
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

// cachePathJoin is a tiny helper to normalize paths under cache; retained for future use.
func cachePathJoin(dir, name string) string {
	return filepath.Join(dir, name)
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
