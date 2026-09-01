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

// loadOrganeCache builds organeRef -> {short,long} from the organe entries of
// the AMO30 archive (see loadAMO30Archive). An unreadable archive or one that
// yields no organes is an error: with an empty map every party name resolves
// to "", derivePartyMajorityFacts drops every party fact, and the party seeds
// fall back to raw refs — which is how prod ended up with "persons" named
// po840056 and three months of zero party facts when the old per-legislature
// Organes.json.zip URL started returning 404.
func loadOrganeCache(ctx context.Context) (map[string]OrganeInfo, error) {
	blob, err := loadAMO30Archive(ctx)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		return nil, fmt.Errorf("open AMO30 archive: %w", err)
	}
	out := make(map[string]OrganeInfo)
	for _, f := range zr.File {
		name := strings.ToLower(f.Name)
		if !strings.HasSuffix(name, ".json") || !strings.Contains(name, "organe") {
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
	if len(out) == 0 {
		return nil, fmt.Errorf("AMO30 archive contains no organe entries (format changed?)")
	}
	return out, nil
}
