package frpolitics

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// The AN publishes every amendment of a legislature (dispositif, exposé
// sommaire, author, group, sort) as one large archive. This is what makes
// amendment votes interpretable (mirror-0dj): the scrutin title alone does not
// say what a sous-amendement DOES, and summarizing the parent bill instead is
// how prod manufactured the false "Panot voted against nationalisation" edge.
const amendementsURLTpl = "https://data.assemblee-nationale.fr/static/openData/repository/%d/loi/amendements_div_legis/Amendements.json.zip"

// AmendementInfo is the voted-object description extracted from the archive.
type AmendementInfo struct {
	Numero         int
	TexteRef       string
	AuthorActorRef string
	AuthorGroupRef string
	Dispositif     string
	Expose         string
	Sort           string
	DateSort       time.Time
}

// amdArchives caches one open zip reader per legislature for the process
// lifetime. The archive is ~300MB compressed with >100k entries, so lookups
// scan the (cheap) central directory and decompress only candidate files.
var amdArchives struct {
	sync.Mutex
	readers map[int]*zip.ReadCloser
}

func amendementsArchive(ctx context.Context, legislature int) (*zip.ReadCloser, error) {
	amdArchives.Lock()
	defer amdArchives.Unlock()
	if r, ok := amdArchives.readers[legislature]; ok {
		return r, nil
	}

	path := filepath.Join(getCacheDir(), fmt.Sprintf("Amendements_%d.json.zip", legislature))
	if _, err := os.Stat(path); err != nil {
		// The AN endpoint drops large transfers regularly; downloadCached has no
		// resume, so retry a few times before declaring the archive unavailable.
		url := fmt.Sprintf(amendementsURLTpl, legislature)
		var body []byte
		var dlErr error
		for attempt := 1; attempt <= 3; attempt++ {
			body, dlErr = downloadCached(ctx, url, getCacheDir(), 30*24*time.Hour)
			if dlErr == nil {
				break
			}
			log.Warn().Err(dlErr).Int("attempt", attempt).Int("legislature", legislature).
				Msg("frpolitics: amendements archive download failed")
		}
		if dlErr != nil {
			return nil, fmt.Errorf("amendements archive for legislature %d unavailable: %w", legislature, dlErr)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return nil, fmt.Errorf("caching amendements archive: %w", err)
		}
	}

	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open amendements archive %s: %w", path, err)
	}
	if amdArchives.readers == nil {
		amdArchives.readers = make(map[int]*zip.ReadCloser)
	}
	amdArchives.readers[legislature] = r
	return r, nil
}

// LookupAmendement finds the amendment voted by a scrutin: entries whose
// filename ends in N<numero>.json (the uid encodes texte + numero), then
// disambiguated by the sort date matching the scrutin date. Returns nil when
// no candidate — or more than one — matches: an ambiguous voted object must
// gate, never guess.
func LookupAmendement(ctx context.Context, legislature, numero int, scrutinDate *time.Time) *AmendementInfo {
	r, err := amendementsArchive(ctx, legislature)
	if err != nil {
		log.Warn().Err(err).Msg("frpolitics: amendement lookup impossible")
		return nil
	}
	suffix := fmt.Sprintf("N%06d.json", numero)
	var candidates []*AmendementInfo
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, suffix) {
			continue
		}
		info, err := parseAmendementEntry(f)
		if err != nil {
			continue
		}
		if scrutinDate != nil && !info.DateSort.IsZero() &&
			info.DateSort.Format("2006-01-02") != scrutinDate.Format("2006-01-02") {
			continue
		}
		candidates = append(candidates, info)
	}
	if len(candidates) != 1 {
		if len(candidates) > 1 {
			log.Warn().Int("numero", numero).Int("candidates", len(candidates)).
				Msg("frpolitics: ambiguous amendement lookup; gating")
		}
		return nil
	}
	return candidates[0]
}

var tagRe = regexp.MustCompile(`<[^>]+>`)
var spaceRe = regexp.MustCompile(`\s+`)

// cleanAmdText decodes the archive's doubly-escaped HTML (&#x00C0;, &nbsp;)
// and strips markup down to plain prose.
func cleanAmdText(s string) string {
	s = html.UnescapeString(html.UnescapeString(s))
	s = tagRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, " ", " ")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

var purposeLeadRe = regexp.MustCompile(`(?i)^ce (sous-)?amendement (vise à|tend à|a pour objet d[e'']\s*|propose d[e'']\s*)`)

// purposePhrase turns the exposé sommaire (or, failing that, the dispositif)
// into a "visant à ..." clause for the fact text.
func purposePhrase(info *AmendementInfo) string {
	if e := strings.TrimSpace(info.Expose); e != "" {
		if m := purposeLeadRe.FindString(e); m != "" {
			rest := strings.TrimSuffix(strings.TrimSpace(e[len(m):]), ".")
			return capRunes("visant à "+rest, 220)
		}
		return capRunes("dont l'objet est : « "+e+" »", 240)
	}
	if d := strings.TrimSpace(info.Dispositif); d != "" {
		return capRunes("dont le dispositif est : « "+d+" »", 240)
	}
	return ""
}

func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// parseAmendementEntry decodes one archive entry.
func parseAmendementEntry(f *zip.File) (*AmendementInfo, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var raw struct {
		Amendement struct {
			Uid                string `json:"uid"`
			TexteLegislatifRef string `json:"texteLegislatifRef"`
			Identification     struct {
				NumeroLong string `json:"numeroLong"`
			} `json:"identification"`
			Signataires struct {
				Auteur struct {
					ActeurRef          any `json:"acteurRef"`
					GroupePolitiqueRef any `json:"groupePolitiqueRef"`
				} `json:"auteur"`
			} `json:"signataires"`
			Corps struct {
				ContenuAuteur struct {
					Dispositif     string `json:"dispositif"`
					ExposeSommaire string `json:"exposeSommaire"`
				} `json:"contenuAuteur"`
			} `json:"corps"`
			CycleDeVie struct {
				Sort     string `json:"sort"`
				DateSort string `json:"dateSort"`
			} `json:"cycleDeVie"`
		} `json:"amendement"`
	}
	if err := json.NewDecoder(io.LimitReader(rc, 4<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	a := raw.Amendement
	num := 0
	fmt.Sscanf(a.Identification.NumeroLong, "%d", &num)
	info := &AmendementInfo{
		Numero:         num,
		TexteRef:       a.TexteLegislatifRef,
		AuthorActorRef: asString(a.Signataires.Auteur.ActeurRef),
		AuthorGroupRef: asString(a.Signataires.Auteur.GroupePolitiqueRef),
		Dispositif:     cleanAmdText(a.Corps.ContenuAuteur.Dispositif),
		Expose:         cleanAmdText(a.Corps.ContenuAuteur.ExposeSommaire),
		Sort:           a.CycleDeVie.Sort,
	}
	if t, err := time.Parse(time.RFC3339, a.CycleDeVie.DateSort); err == nil {
		info.DateSort = t
	}
	return info, nil
}
