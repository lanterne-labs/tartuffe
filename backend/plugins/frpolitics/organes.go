package frpolitics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// loadOrganeCacheForLegislature loads organe name mappings from the official static dataset.
// It attempts to fetch Organes.json.zip and builds a map of organeRef -> {short,long}.
// On any error, returns an empty map.
func loadOrganeCacheForLegislature(ctx context.Context, legislature int) map[string]OrganeInfo {
	out := make(map[string]OrganeInfo)
	url := fmt.Sprintf("https://data.assemblee-nationale.fr/static/openData/repository/%d/organes/Organes.json.zip", legislature)
	body, err := downloadCached(ctx, url, getCacheDir())
	if err != nil {
		return out
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return out
	}
	// Tolerant per-entry decoder: expect each JSON to contain a top-level 'organe' object with identifiers and labels
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".json") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, 1<<20)) // 1MB per entry limit
		rc.Close()
		if err != nil {
			continue
		}
		var rec struct {
			Organe struct {
				Uid           string `json:"uid"`
				Code          string `json:"code"`
				Sigle         string `json:"sigle"`
				Libelle       string `json:"libelle"`
				LibelleAbrege string `json:"libelleAbrege"`
				Nom           string `json:"nom"`
				NomComplet    string `json:"nomComplet"`
				Titre         string `json:"titre"`
				TitreAbrege   string `json:"titreAbrege"`
			} `json:"organe"`
		}
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		ref := strings.TrimSpace(rec.Organe.Uid)
		if ref == "" {
			continue
		}
		short := firstNonEmpty(rec.Organe.Sigle, rec.Organe.LibelleAbrege, rec.Organe.TitreAbrege)
		long := firstNonEmpty(rec.Organe.NomComplet, rec.Organe.Libelle, rec.Organe.Nom, rec.Organe.Titre)
		if short == "" && long == "" {
			continue
		}
		out[ref] = OrganeInfo{OrganeRef: ref, ShortName: short, LongName: long}
	}
	return out
}

// ensureOrganeInfo fetches organe info for a specific ref if not in cache, using API as fallback.
func ensureOrganeInfo(ctx context.Context, legislature int, orgRef string, cache map[string]OrganeInfo) OrganeInfo {
	if info, ok := cache[orgRef]; ok {
		return info
	}
	if info, ok := FetchOrganeInfo(ctx, legislature, orgRef); ok {
		cache[orgRef] = info
		return info
	}
	// Fallback: just the ref
	return OrganeInfo{OrganeRef: orgRef}
}
