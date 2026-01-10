package frpolitics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClient is a lightweight HTTP client with sane timeout
var httpClient = &http.Client{Timeout: 30 * time.Second}

// scrutinIndex represents a minimal structure of a scrutins listing item
type scrutinIndex struct {
	// ID is a unique identifier for the scrutin (e.g., "SCRUTIN_1234")
	ID string `json:"id"`
	// URL is the direct URL to the scrutin JSON file
	URL string `json:"url"`
	// Legislature number
	Legislature int `json:"legislature"`
}

// OrganeInfo represents minimal mapping from organeRef to party name
type OrganeInfo struct {
	// OrganeRef is the Assemblée group reference id
	OrganeRef string
	// ShortName is a short label for the group/party (e.g., "LR")
	ShortName string
	// LongName is the full group/party name
	LongName string
}

// FetchJSON downloads and decodes JSON into target
func FetchJSON(ctx context.Context, url string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed creating request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("unexpected status %d for %s: %s", resp.StatusCode, url, string(b))
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("failed decoding json from %s: %w", url, err)
	}
	return nil
}

// ResolveOrganeNames resolves an organeRef into short and long names using an official endpoint
// For now, this function expects a map pre-fetched by the caller
func ResolveOrganeNames(organeRef string, cache map[string]OrganeInfo) (OrganeInfo, bool) {
	info, ok := cache[organeRef]
	return info, ok
}

// OrganeAPIURL returns the official API URL for an organe within a legislature
func OrganeAPIURL(legislature int, organeRef string) string {
	// organeRef for groups is typically a slug; we do not transform it here
	return fmt.Sprintf("https://www.assemblee-nationale.fr/dyn/%d/organes/api/%s", legislature, organeRef)
}

// FetchOrganeInfo attempts to resolve an organe into short/long names from the official API.
// Returns ok=false if not resolvable.
func FetchOrganeInfo(ctx context.Context, legislature int, organeRef string) (OrganeInfo, bool) {
	url := OrganeAPIURL(legislature, organeRef)
	var resp struct {
		Libelle       string `json:"libelle"`
		LibelleAbrege string `json:"libelleAbrege"`
		Code          string `json:"code"`
		Nom           string `json:"nom"`
		NomComplet    string `json:"nomComplet"`
		Titre         string `json:"titre"`
		TitreAbrege   string `json:"titreAbrege"`
	}
	if err := FetchJSON(ctx, url, &resp); err != nil {
		return OrganeInfo{}, false
	}
	short := firstNonEmpty(resp.LibelleAbrege, resp.TitreAbrege)
	long := firstNonEmpty(resp.NomComplet, resp.Libelle, resp.Nom, resp.Titre)
	return OrganeInfo{OrganeRef: organeRef, ShortName: short, LongName: long}, true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
