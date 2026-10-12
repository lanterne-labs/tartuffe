package twitter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/search"

	"github.com/rs/zerolog/log"
)

// discoverySearchCooldown is how long a person is left alone after a
// discovery search, successful or not.
const discoverySearchCooldown = 7 * 24 * time.Hour

// DiscoveryOptions configures handle auto-discovery.
type DiscoveryOptions struct {
	DryRun       bool
	PersonFilter string  // if set, only discover for this person
	Force        bool    // bypass cooldown and existing-handle checks
	TargetIDs    []int64 // if non-empty, only discover for these person IDs (queue-scoped)
}

// userInfo holds live profile data fetched from twitterapi.io.
type userInfo struct {
	Name        string
	Description string
	IsVerified  bool
	Followers   int
}

// fetchUserInfo calls twitterapi.io to get live profile info for a handle.
// Returns nil (no error) if the API key is absent or the handle doesn't exist.
func fetchUserInfo(ctx context.Context, apiKey, handle string) (*userInfo, error) {
	if apiKey == "" {
		return nil, nil
	}

	reqURL := "https://api.twitterapi.io/twitter/user/info?userName=" + url.QueryEscape(handle)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-API-Key", apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API status %d: %s", resp.StatusCode, body)
	}

	var apiResp struct {
		Data struct {
			Name           string `json:"name"`
			Description    string `json:"description"`
			IsVerified     bool   `json:"isVerified"`
			IsBlueVerified bool   `json:"isBlueVerified"`
			Followers      int    `json:"followers"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &userInfo{
		Name:        apiResp.Data.Name,
		Description: apiResp.Data.Description,
		IsVerified:  apiResp.Data.IsVerified || apiResp.Data.IsBlueVerified,
		Followers:   apiResp.Data.Followers,
	}, nil
}

// candidate is a potential Twitter handle found in search results.
type candidate struct {
	Handle     string
	Direct     bool      // true when the SERP result URL is itself a twitter.com/x.com profile page
	Bio        string    // SERP snippet; used as fallback when UserInfo is nil
	ProfileURL string    // the twitter.com/x.com URL when Direct is true
	UserInfo   *userInfo // live profile data from twitterapi.io (nil if unavailable)
}

var (
	// profileURLRe matches x.com/username or twitter.com/username URLs.
	profileURLRe = regexp.MustCompile(`(?:x\.com|twitter\.com)/([A-Za-z0-9_]{1,15})(?:[/?#]|$)`)
	// mentionRe matches @handle mentions.
	mentionRe = regexp.MustCompile(`@([A-Za-z0-9_]{1,15})\b`)
	// handleLineRe matches the HANDLE: line of the LLM reply.
	handleLineRe = regexp.MustCompile(`(?i)HANDLE:\s*@?(\S+)`)
	// reservedPaths are twitter.com paths that look like handles but are not.
	reservedPaths = map[string]bool{
		"search": true, "i": true, "hashtag": true, "intent": true,
		"home": true, "explore": true, "settings": true, "status": true,
	}
)

func extractHandlesFromWeb(results []search.SearchResult) []candidate {
	seen := make(map[string]bool)
	var candidates []candidate

	for _, r := range results {
		// The result URL itself is a direct profile link (highest confidence).
		if match := profileURLRe.FindStringSubmatch(r.URL); match != nil {
			handle := strings.ToLower(match[1])
			if !reservedPaths[handle] && !seen[handle] {
				seen[handle] = true
				candidates = append(candidates, candidate{
					Handle:     handle,
					Direct:     true,
					Bio:        r.Snippet,
					ProfileURL: r.URL,
				})
				continue
			}
		}

		// URLs mentioned in title and snippet (lower confidence).
		for _, text := range []string{r.Title, r.Snippet} {
			for _, match := range profileURLRe.FindAllStringSubmatch(text, -1) {
				handle := strings.ToLower(match[1])
				if !reservedPaths[handle] && !seen[handle] {
					seen[handle] = true
					candidates = append(candidates, candidate{Handle: handle})
				}
			}
		}
		// @mentions in title and snippet.
		for _, text := range []string{r.Title, r.Snippet} {
			for _, match := range mentionRe.FindAllStringSubmatch(text, -1) {
				handle := strings.ToLower(match[1])
				if !seen[handle] {
					seen[handle] = true
					candidates = append(candidates, candidate{Handle: handle})
				}
			}
		}
	}

	return candidates
}

// Discover searches for and links Twitter handles for persons who don't have
// one. Returns the number of handles confirmed.
func Discover(ctx context.Context, d *deps.Deps, opts DiscoveryOptions) (int, error) {
	storer := d.Storer
	store := NewStore(storer)
	allPersons, err := storer.GetAllPersonRecords(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get person records: %w", err)
	}

	var persons []core.PersonRecord
	switch {
	case len(opts.TargetIDs) > 0:
		idSet := make(map[int64]bool, len(opts.TargetIDs))
		for _, id := range opts.TargetIDs {
			idSet[id] = true
		}
		for _, p := range allPersons {
			if idSet[p.ID] {
				persons = append(persons, p)
			}
		}
	case opts.PersonFilter != "":
		person, err := core.ResolvePersonID(ctx, storer, opts.PersonFilter, 0.8)
		if err != nil {
			return 0, fmt.Errorf("failed to resolve person: %w", err)
		}
		persons = []core.PersonRecord{person}
	default:
		persons = allPersons
	}

	if len(persons) == 0 {
		log.Info().Msg("twitter discovery: no persons to process")
		return 0, nil
	}

	searcher, err := d.WebSearcher()
	if err != nil {
		return 0, fmt.Errorf("failed to initialize search provider: %w", err)
	}

	// The key is not required here; an unset OPEN_AI_API falls back to OpenAI.
	llmCfg := llm.ConfigFromEnv()
	if llmCfg.BaseURL == "" {
		llmCfg.BaseURL = llm.DefaultBaseURL
	}
	dc := &discoverer{
		store:         store,
		searcher:      searcher,
		client:        llmCfg.NewClient("twitter-discovery"),
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		twitterAPIKey: os.Getenv("TWITTER_API_IO_KEY"),
	}

	var discovered int
	for i, person := range persons {
		if err := ctx.Err(); err != nil {
			return discovered, fmt.Errorf("twitter discovery stopped: %w", err)
		}
		if i > 0 {
			time.Sleep(1 * time.Second) // rate limit between persons
		}
		existingHandles, err := store.GetHandlesByPersonID(ctx, person.ID)
		if err != nil {
			log.Error().Err(err).Str("person", person.Name).Msg("Failed to check existing handles")
			continue
		}
		if len(existingHandles) > 0 && opts.PersonFilter == "" && !opts.Force {
			log.Debug().Str("person", person.Name).Int("handles", len(existingHandles)).Msg("Skipping person with existing handles")
			continue
		}

		if opts.PersonFilter == "" && !opts.Force {
			searchedAt, err := store.GetSearchedAt(ctx, person.ID)
			if err != nil {
				log.Error().Err(err).Str("person", person.Name).Msg("Failed to check twitter_searched_at")
				continue
			}
			if searchedAt != nil && time.Since(*searchedAt) < discoverySearchCooldown {
				log.Debug().Str("person", person.Name).Time("searched_at", *searchedAt).Msg("Skipping recently searched person")
				continue
			}
		}

		log.Info().Int("progress", i+1).Int("total", len(persons)).Str("person", person.Name).Msg("Searching for Twitter handle")

		confirmed, err := dc.discover(ctx, person, opts)
		if err != nil {
			return discovered, err
		}
		if confirmed {
			discovered++
		}
	}

	log.Info().Int("discovered", discovered).Int("total_persons", len(persons)).Msg("Twitter discovery completed")
	return discovered, nil
}

// discoverer holds what the per-person handle discovery needs.
type discoverer struct {
	store         *Store
	searcher      search.WebSearcher
	client        *llm.Client
	httpClient    *http.Client // Wikipedia lookups
	twitterAPIKey string
}

// discover searches the person's handle, has the LLM confirm it and links it.
// It reports whether a handle was confirmed. The person enters the discovery
// cooldown (MarkSearched) on every outcome but an unreachable LLM
// (llm.ErrUnavailable): a failed search or a rejected LLM call would fail the
// same way on the next run and re-spend a web search each time. The only
// error returned is that unreachable LLM, which aborts the discovery: every
// further person would burn a web search for nothing, and the person is left
// to the next run; so is an LLM call cut by a cancelled ctx.
func (dc *discoverer) discover(ctx context.Context, person core.PersonRecord, opts DiscoveryOptions) (bool, error) {
	markSearched := func() {
		if err := dc.store.MarkSearched(ctx, person.ID); err != nil {
			log.Warn().Err(err).Str("person", person.Name).Msg("Failed to mark person as twitter-searched")
		}
	}

	query := fmt.Sprintf(`"%s" twitter site:x.com OR site:twitter.com`, person.Name)
	results, err := dc.searcher.SearchWeb(query, 10)
	if err != nil {
		log.Error().Err(err).Str("person", person.Name).Msg("Search failed")
		markSearched()
		return false, nil
	}

	wikiSummary, wikiErr := search.FetchWikipediaSummary(ctx, dc.httpClient, person.Name)
	if wikiErr != nil {
		log.Warn().Err(wikiErr).Str("person", person.Name).Msg("Wikipedia lookup failed (continuing without it)")
	}

	candidates := extractHandlesFromWeb(results)

	// Enrich direct SERP hits with live profile data from twitterapi.io.
	for idx, c := range candidates {
		if !c.Direct {
			continue
		}
		info, err := fetchUserInfo(ctx, dc.twitterAPIKey, c.Handle)
		if err != nil {
			log.Warn().Err(err).Str("handle", c.Handle).Msg("Failed to fetch Twitter user info")
			continue
		}
		if info != nil {
			candidates[idx].UserInfo = info
			log.Debug().Str("handle", c.Handle).Str("name", info.Name).Int("followers", info.Followers).Msg("Fetched Twitter user info")
		}
	}

	if len(candidates) == 0 && wikiSummary == nil {
		log.Info().Str("person", person.Name).Msg("No candidate handles found and no Wikipedia bio")
		markSearched()
		return false, nil
	}

	if len(candidates) > 0 {
		var directHandles, otherHandles []string
		for _, c := range candidates {
			if c.Direct {
				directHandles = append(directHandles, c.Handle)
			} else {
				otherHandles = append(otherHandles, c.Handle)
			}
		}
		log.Info().Str("person", person.Name).Strs("direct_hits", directHandles).Strs("mentioned", otherHandles).Msg("Found candidate handles")
	}
	if wikiSummary != nil {
		log.Debug().Str("person", person.Name).Str("wiki_title", wikiSummary.Title).Str("lang", wikiSummary.Lang).Msg("Got Wikipedia bio")
	}

	confirmedHandle, content, err := askHandle(ctx, dc.client, person.Name, results, candidates, wikiSummary)
	if errors.Is(err, llm.ErrUnavailable) {
		return false, fmt.Errorf("twitter discovery for %s: %w", person.Name, err)
	}
	if err != nil && ctx.Err() != nil {
		// Cut by the cancelled run: the person is searched again next run.
		return false, fmt.Errorf("twitter discovery for %s stopped: %w", person.Name, ctx.Err())
	}
	markSearched()
	if err != nil {
		log.Error().Err(err).Str("person", person.Name).Msg("LLM request failed")
		return false, nil
	}

	if confirmedHandle == "" {
		log.Info().Str("person", person.Name).Str("llm_response", content).Msg("No confirmed handle")
		return false, nil
	}

	// Hard guardrail: never link a handle without a live Twitter bio
	// confirming it. The LLM can be tricked by SERP-adjacent results when
	// no bio is available (Alexey Meshkov → @ambrusfrance was linked when
	// TwitterAPI.io was 402'd and UserInfo was nil for all candidates).
	// Without a bio, prefer "no link" — the next discovery run retries
	// once the API is healthy.
	if !handleConfirmedByBio(confirmedHandle, candidates) {
		log.Warn().Str("person", person.Name).Str("rejected_handle", confirmedHandle).
			Msg("Discovery guardrail: no live Twitter bio confirms the handle; refusing to link (will retry next run)")
		return false, nil
	}

	log.Info().Str("person", person.Name).Str("handle", confirmedHandle).Bool("dry_run", opts.DryRun).Msg("Confirmed Twitter handle")

	if !opts.DryRun {
		if err := dc.store.AddHandle(ctx, person.ID, confirmedHandle); err != nil {
			log.Error().Err(err).Str("person", person.Name).Str("handle", confirmedHandle).Msg("Failed to save handle")
			return false, nil
		}
		log.Info().Str("person", person.Name).Str("handle", confirmedHandle).Msg("Saved Twitter handle")
	}
	return true, nil
}

// askHandle asks the LLM for the official handle of personName from the
// search evidence. It returns the handle in lower case, or "" when the reply
// is NONE or has no HANDLE line. reply is the trimmed LLM reply, for the log.
func askHandle(ctx context.Context, client *llm.Client, personName string, results []search.SearchResult, candidates []candidate, wiki *search.WikipediaSummary) (handle, reply string, err error) {
	content, _, err := client.Chat(ctx, llm.ChatRequest{
		Temperature: 0.1,
		MaxTokens:   512,
		Messages: []llm.ChatMessage{
			{
				Role: "system",
				Content: "You are an expert at identifying official Twitter/X accounts for public figures. " +
					"You cross-reference multiple sources to confirm the handle belongs to the exact person, " +
					"not a homonym, parody, or fan account.",
			},
			{Role: "user", Content: buildDiscoveryPrompt(personName, results, candidates, wiki)},
		},
		Task: llm.TaskHandle,
	})
	if err != nil {
		return "", "", err
	}
	content = strings.TrimSpace(content)
	match := handleLineRe.FindStringSubmatch(content)
	if match == nil || strings.EqualFold(strings.TrimRight(match[1], ".,;"), "NONE") {
		return "", content, nil
	}
	return strings.ToLower(strings.TrimRight(match[1], ".,;")), content, nil
}

// handleConfirmedByBio is the deterministic post-LLM guardrail: a handle is
// linkable only when a live bio from TwitterAPI.io was fetched for it, since
// that bio is the LLM's only way to falsify a SERP-adjacent false positive.
// A handle absent from the candidate list is an LLM hallucination.
func handleConfirmedByBio(handle string, candidates []candidate) bool {
	for _, c := range candidates {
		if strings.EqualFold(c.Handle, handle) {
			return c.UserInfo != nil
		}
	}
	return false
}

// buildDiscoveryPrompt assembles the verification prompt: search results,
// extracted candidates and, when available, the Wikipedia bio, so the model
// can cross-reference identity signals.
func buildDiscoveryPrompt(personName string, results []search.SearchResult, candidates []candidate, wiki *search.WikipediaSummary) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Identify the official Twitter/X handle for: **%s**\n\n", personName)

	if wiki != nil {
		fmt.Fprintf(&b, "## Wikipedia Profile\n")
		if wiki.Description != "" {
			fmt.Fprintf(&b, "Short description: %s\n", wiki.Description)
		}
		fmt.Fprintf(&b, "Bio: %s\n\n", wiki.Extract)
	}

	var directHits, others []candidate
	for _, c := range candidates {
		if c.Direct {
			directHits = append(directHits, c)
		} else {
			others = append(others, c)
		}
	}

	if len(directHits) > 0 {
		fmt.Fprintln(&b, "## HIGH-CONFIDENCE CANDIDATES (Google returned direct Twitter/X profile URLs)")
		fmt.Fprintln(&b, "These profiles were directly listed by Google — treat them as very strong candidates.")
		fmt.Fprintln(&b)
		for i, c := range directHits {
			fmt.Fprintf(&b, "%d. Handle: @%s\n   Profile URL: %s\n", i+1, c.Handle, c.ProfileURL)
			if c.UserInfo != nil {
				if c.UserInfo.Name != "" {
					fmt.Fprintf(&b, "   Display name: %s\n", c.UserInfo.Name)
				}
				if c.UserInfo.Description != "" {
					fmt.Fprintf(&b, "   Twitter bio: %s\n", c.UserInfo.Description)
				}
				if c.UserInfo.IsVerified {
					fmt.Fprintf(&b, "   Verified: yes\n")
				}
				if c.UserInfo.Followers > 0 {
					fmt.Fprintf(&b, "   Followers: %d\n", c.UserInfo.Followers)
				}
			} else if c.Bio != "" {
				fmt.Fprintf(&b, "   Google snippet: %s\n", c.Bio)
			}
		}
		fmt.Fprintln(&b)
	}

	if len(others) > 0 {
		var otherHandles []string
		for _, c := range others {
			otherHandles = append(otherHandles, "@"+c.Handle)
		}
		fmt.Fprintf(&b, "## OTHER CANDIDATE HANDLES (mentioned in search snippets)\n%s\n\n", strings.Join(otherHandles, ", "))
	}

	if len(candidates) == 0 {
		fmt.Fprintln(&b, "## Candidate handles\nNone extracted — no handle can be confirmed; reply NONE.")
		fmt.Fprintln(&b)
	}

	if len(results) > 0 {
		fmt.Fprintf(&b, "## Full Google Search Results (query: \"%s twitter\")\n", personName)
		for i, r := range results {
			fmt.Fprintf(&b, "%d. Title: %s\n   Snippet: %s\n   URL: %s\n", i+1, r.Title, r.Snippet, r.URL)
		}
		fmt.Fprintln(&b)
	}

	b.WriteString(
		"## Task\n" +
			"Using all the evidence above, determine the official Twitter/X handle for " + personName + ".\n" +
			"Rules:\n" +
			"- The account must unambiguously belong to the exact person (not a homonym, parody, or fan account).\n" +
			"- HIGH-CONFIDENCE CANDIDATES (direct Google hits) are strong signals — verify using the Twitter bio and Wikipedia profile.\n" +
			"- Cross-check name, occupation, country, and political party across all sources.\n" +
			"- **When in doubt, reply NONE.** It is far better to return nothing than to link the wrong account.\n" +
			"- If the bio/description does not clearly match the person's known occupation or affiliation, reply NONE.\n" +
			"- A generic name or incomplete evidence is not enough — require clear confirmation.\n" +
			"- Only a handle whose profile is in the candidate sections above can be confirmed. A handle named inside the bio or text of another account is not a candidate: do not return it, reply NONE.\n\n" +
			"Reply in EXACTLY this format (two lines):\n" +
			"REASONING: <one sentence explaining your choice>\n" +
			"HANDLE: @username\n" +
			"or:\n" +
			"REASONING: <why not found or uncertain>\n" +
			"HANDLE: NONE",
	)

	return b.String()
}
