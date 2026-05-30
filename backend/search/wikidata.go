package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/rs/zerolog/log"
)

// WikidataPartyResult holds the result of a Wikidata party affiliation lookup.
type WikidataPartyResult struct {
	EntityID   string
	PartyNames []string // French labels of current parties (P102 without end_time)
}

// wbSearchResponse is the response from wbsearchentities.
type wbSearchResponse struct {
	Search []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"search"`
}

// p102Statement represents a P102 statement from the Wikidata REST API.
type p102Statement struct {
	Value      statementValue   `json:"value"`
	Qualifiers []qualifierEntry `json:"qualifiers"`
}

type qualifierEntry struct {
	Property struct {
		ID string `json:"id"`
	} `json:"property"`
	Value struct {
		Content json.RawMessage `json:"content"`
	} `json:"value"`
}

type qualifierTimeContent struct {
	Time string `json:"time"` // e.g. "+2017-00-00T00:00:00Z"
}

type statementValue struct {
	Type    string `json:"type"`
	Content any    `json:"content"`
}

// LookupPartyAffiliation searches Wikidata for a person and returns their current party affiliations.
func LookupPartyAffiliation(ctx context.Context, httpClient *http.Client, personName string) (*WikidataPartyResult, error) {
	// Step 1: Search for the entity
	entityID, err := searchEntity(ctx, httpClient, personName)
	if err != nil {
		return nil, fmt.Errorf("search entity %q: %w", personName, err)
	}
	if entityID == "" {
		return nil, nil // no match found
	}

	// Step 2: Fetch P102 (member of political party) statements
	partyEntityIDs, err := fetchP102Statements(ctx, httpClient, entityID)
	if err != nil {
		return nil, fmt.Errorf("fetch P102 for %s: %w", entityID, err)
	}
	if len(partyEntityIDs) == 0 {
		return &WikidataPartyResult{EntityID: entityID}, nil
	}

	// Step 3: Get French labels for each party
	var partyNames []string
	for _, partyID := range partyEntityIDs {
		label, err := fetchLabel(ctx, httpClient, partyID, "fr")
		if err != nil {
			log.Warn().Err(err).Str("party_id", partyID).Msg("Failed to fetch party label, trying English")
			label, err = fetchLabel(ctx, httpClient, partyID, "en")
			if err != nil {
				log.Warn().Err(err).Str("party_id", partyID).Msg("Failed to fetch party label in any language")
				continue
			}
		}
		partyNames = append(partyNames, label)
	}

	return &WikidataPartyResult{
		EntityID:   entityID,
		PartyNames: partyNames,
	}, nil
}

func searchEntity(ctx context.Context, httpClient *http.Client, name string) (string, error) {
	u := fmt.Sprintf("https://www.wikidata.org/w/api.php?action=wbsearchentities&search=%s&language=fr&type=item&format=json",
		url.QueryEscape(name))

	body, err := doGet(ctx, httpClient, u)
	if err != nil {
		return "", err
	}

	var resp wbSearchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("unmarshal search response: %w", err)
	}

	if len(resp.Search) == 0 {
		return "", nil
	}
	return resp.Search[0].ID, nil
}

type parsedStatement struct {
	partyID string
	endTime string // P582 value, empty if no end_time
}

func fetchP102Statements(ctx context.Context, httpClient *http.Client, entityID string) ([]string, error) {
	u := fmt.Sprintf("https://www.wikidata.org/w/rest.php/wikibase/v1/entities/items/%s/statements?property=P102", entityID)

	body, err := doGet(ctx, httpClient, u)
	if err != nil {
		return nil, err
	}

	// The REST API returns {"P102": [...statements...]}
	var envelope map[string][]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal P102 envelope: %w", err)
	}

	rawStatements := envelope["P102"]
	if len(rawStatements) == 0 {
		return nil, nil
	}

	var parsed []parsedStatement
	for _, raw := range rawStatements {
		var stmt p102Statement
		if err := json.Unmarshal(raw, &stmt); err != nil {
			log.Warn().Err(err).Msg("Failed to parse P102 statement")
			continue
		}

		partyID := extractEntityID(stmt.Value)
		if partyID == "" {
			continue
		}

		ps := parsedStatement{partyID: partyID}
		for _, q := range stmt.Qualifiers {
			if q.Property.ID == "P582" {
				var tc qualifierTimeContent
				if err := json.Unmarshal(q.Value.Content, &tc); err == nil {
					ps.endTime = tc.Time
				}
				break
			}
		}
		parsed = append(parsed, ps)
	}

	// Prefer current affiliations (no end_time)
	var currentIDs []string
	for _, p := range parsed {
		if p.endTime == "" {
			currentIDs = append(currentIDs, p.partyID)
		}
	}
	if len(currentIDs) > 0 {
		return currentIDs, nil
	}

	// Fallback: all affiliations have end_time, pick the most recent one
	var latest parsedStatement
	for _, p := range parsed {
		if p.endTime > latest.endTime {
			latest = p
		}
	}
	if latest.partyID != "" {
		log.Debug().Str("entity", entityID).Str("end_time", latest.endTime).Msg("No current affiliation, using most recent ended one")
		return []string{latest.partyID}, nil
	}

	return nil, nil
}

func extractEntityID(v statementValue) string {
	if v.Type != "value" {
		return ""
	}
	// Content can be a string (entity ID) or a map with "id" field
	switch c := v.Content.(type) {
	case string:
		return c
	case map[string]any:
		if id, ok := c["id"].(string); ok {
			return id
		}
	}
	return ""
}

func fetchLabel(ctx context.Context, httpClient *http.Client, entityID string, lang string) (string, error) {
	u := fmt.Sprintf("https://www.wikidata.org/w/rest.php/wikibase/v1/entities/items/%s/labels", entityID)

	body, err := doGet(ctx, httpClient, u)
	if err != nil {
		return "", err
	}

	var labels map[string]string
	if err := json.Unmarshal(body, &labels); err != nil {
		return "", fmt.Errorf("unmarshal labels: %w", err)
	}

	label, ok := labels[lang]
	if !ok {
		return "", fmt.Errorf("no %s label for %s", lang, entityID)
	}
	return label, nil
}

func doGet(ctx context.Context, httpClient *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "DigitalMirror/1.0 (fact-checking tool)")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP GET %s: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}
