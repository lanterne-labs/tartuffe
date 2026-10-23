package polls

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"digital-mirror/backend/httpx"
	"digital-mirror/backend/metrics"

	"github.com/go-chi/chi/v5"
)

// handleListPolls lists the events, the featured one first.
func (s *Service) handleListPolls(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListEvents(r.Context())
	if err != nil {
		httpx.InternalError(w, err, "failed to list polls")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, events)
}

// pollEvent loads the event of the {slug} path parameter, answering 404 itself.
func (s *Service) pollEvent(w http.ResponseWriter, r *http.Request) (*Event, bool) {
	e, err := s.store.GetEvent(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		httpx.InternalError(w, err, "failed to get poll")
		return nil, false
	}
	if e == nil {
		httpx.WriteError(w, http.StatusNotFound, "no such poll")
		return nil, false
	}
	return e, true
}

// handleGetPoll returns the event, its results and the open windows.
func (s *Service) handleGetPoll(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	tallies, turnout, err := s.results.get(r.Context(), e.ID, s.tallies)
	if err != nil {
		httpx.InternalError(w, err, "failed to get poll tallies")
		return
	}
	now := time.Now()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"event":   e,
		"results": Compute(e, tallies, turnout, now),
		"windows": windowsResponse(now),
	})
}

// windowInfo is an open window: its scope is what the browser proves under.
type windowInfo struct {
	Window
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func windowsResponse(now time.Time) []windowInfo {
	out := []windowInfo{}
	for _, w := range OpenWindows(now) {
		out = append(out, windowInfo{Window: w, Start: w.Start(), End: w.End()})
	}
	return out
}

// Codes of a refused ballot.
const (
	codeClosed      = "closed"
	codeInvalid     = "invalid_ballot"
	codeStaleSeq    = "stale_seq"
	codeUnknownRoot = "unknown_root"
)

// maxBallotBody bounds a ballot (a proof and a few ids).
const maxBallotBody = 16 << 10

// handleCastBallot takes an anonymous ballot. It asks nothing of who sends
// it: the proof says a voter of the window cast it, and nothing else.
func (s *Service) handleCastBallot(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	count := func(outcome string) { metrics.BallotChangesTotal.WithLabelValues(outcome).Inc() }
	var b AnonBallot
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBallotBody)).Decode(&b); err != nil {
		count(codeInvalid)
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.CodedError{Error: "invalid ballot: " + err.Error(), Code: codeInvalid})
		return
	}
	candidates, err := s.store.CandidateIDs(r.Context(), e.ID)
	if err != nil {
		httpx.InternalError(w, err, "failed to get candidates")
		return
	}
	now := time.Now()
	if err := CheckAnonBallot(e, candidates, b, now); err != nil {
		code := codeInvalid
		if errors.Is(err, ErrClosed) {
			code = codeClosed
		}
		count(code)
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.CodedError{Error: err.Error(), Code: code})
		return
	}
	err = s.box.Cast(r.Context(), b)
	var stale *StaleSeqError
	switch {
	case errors.As(err, &stale):
		count(codeStaleSeq)
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": codeStaleSeq, "last_seq": stale.Last})
		return
	case errors.Is(err, ErrUnknownRoot):
		count(codeUnknownRoot)
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.CodedError{Error: err.Error(), Code: codeUnknownRoot})
		return
	case errors.Is(err, ErrBallot):
		count(codeInvalid)
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.CodedError{Error: err.Error(), Code: codeInvalid})
		return
	case err != nil:
		httpx.InternalError(w, err, "failed to cast ballot")
		return
	}
	count("ok")
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "waiting", "published_by": s.batches.nextBatch(now)})
}

// publicBallot is a published ballot as anyone reads it.
type publicBallot struct {
	Nullifier string  `json:"nullifier"`
	Seq       int     `json:"seq"`
	Kind      string  `json:"kind"`
	Choices   []int64 `json:"choices"`
	Favourite int64   `json:"favourite,omitempty"`
}

// handleListBallots lists the published ballots of an open window, the last
// of each voter: the browser finds its own by its nullifier.
func (s *Service) handleListBallots(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	win, ok := windowParam(w, r)
	if !ok {
		return
	}
	ballots, err := s.box.Ballots(r.Context(), e.ID, win)
	if err != nil {
		httpx.InternalError(w, err, "failed to list ballots")
		return
	}
	out := make([]publicBallot, len(ballots))
	for i, b := range ballots {
		out[i] = publicBallot{Nullifier: b.Proof.Nullifier, Seq: b.Seq, Kind: b.Kind, Choices: b.Choices, Favourite: b.Favourite}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// windowParam reads the {series} and {window} path parameters.
func windowParam(w http.ResponseWriter, r *http.Request) (Window, bool) {
	series, err1 := strconv.Atoi(chi.URLParam(r, "series"))
	index, err2 := strconv.Atoi(chi.URLParam(r, "window"))
	if err1 != nil || err2 != nil || series < 0 || series >= Series {
		httpx.WriteError(w, http.StatusBadRequest, "invalid window")
		return Window{}, false
	}
	return Window{Series: series, Index: index}, true
}

// handleVoterTree returns every commitment of an open window, in order.
func (s *Service) handleVoterTree(w http.ResponseWriter, r *http.Request) {
	win, ok := windowParam(w, r)
	if !ok {
		return
	}
	t, err := s.box.Tree(r.Context(), win)
	if errors.Is(err, ErrWindowNotOpen) {
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		httpx.InternalError(w, err, "failed to get voter tree")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

// handleRegister admits a voting key: the request carries the commitment
// and the proof of a document that binds it (ZKPassport), never an account.
func (s *Service) handleRegister(w http.ResponseWriter, r *http.Request) {
	document, provider, body, ok := s.accounts.ProveDocument(w, r)
	if !ok {
		return
	}
	var p struct {
		Commitment string `json:"commitment"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.Commitment == "" {
		httpx.WriteError(w, http.StatusBadRequest, "commitment required")
		return
	}
	reg, err := s.box.Register(r.Context(), document, provider, p.Commitment)
	switch {
	case errors.Is(err, ErrCommitmentTaken):
		metrics.VoterRegistrationsTotal.WithLabelValues(provider, "taken").Inc()
		httpx.WriteJSON(w, http.StatusConflict, httpx.CodedError{Error: err.Error(), Code: "commitment_taken"})
		return
	case errors.Is(err, ErrBallot):
		metrics.VoterRegistrationsTotal.WithLabelValues(provider, "invalid").Inc()
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		httpx.InternalError(w, err, "failed to register voter")
		return
	}
	metrics.VoterRegistrationsTotal.WithLabelValues(provider, reg.Status).Inc()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status": reg.Status,
		"from":   []windowInfo{{reg.From[0], reg.From[0].Start(), reg.From[0].End()}, {reg.From[1], reg.From[1].Start(), reg.From[1].End()}},
	})
}

// handleLog returns the public log after ?after (a seq), at most ?limit
// entries.
func (s *Service) handleLog(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.box.Log(r.Context(), after, limit)
	if err != nil {
		httpx.InternalError(w, err, "failed to read vote log")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, entries)
}

// readCiphertext reads {"ciphertext"} and the other named fields of a vault
// or link request.
func readCiphertext(w http.ResponseWriter, r *http.Request, into any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*maxCiphertext))
	if err != nil {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return false
	}
	if err := json.Unmarshal(raw, into); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid payload")
		return false
	}
	return true
}

func nameParam(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	v := chi.URLParam(r, key)
	if !vaultName.MatchString(v) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid "+key)
		return "", false
	}
	return v, true
}

func (s *Service) handleVaultGet(w http.ResponseWriter, r *http.Request) {
	lookup, ok := nameParam(w, r, "lookup")
	if !ok {
		return
	}
	c, err := s.box.VaultGet(r.Context(), lookup)
	if err != nil {
		httpx.InternalError(w, err, "failed to read vault")
		return
	}
	if c == "" {
		httpx.WriteError(w, http.StatusNotFound, "nothing under this passkey")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"ciphertext": c})
}

func (s *Service) handleVaultPut(w http.ResponseWriter, r *http.Request) {
	lookup, ok := nameParam(w, r, "lookup")
	if !ok {
		return
	}
	var p struct {
		Ciphertext string `json:"ciphertext"`
		WriteToken string `json:"write_token"`
	}
	if !readCiphertext(w, r, &p) {
		return
	}
	if p.Ciphertext == "" || len(p.Ciphertext) > maxCiphertext || !vaultName.MatchString(p.WriteToken) {
		httpx.WriteError(w, http.StatusBadRequest, "ciphertext and write_token required")
		return
	}
	err := s.box.VaultPut(r.Context(), lookup, p.Ciphertext, p.WriteToken)
	if errors.Is(err, ErrVaultWrite) {
		httpx.WriteError(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		httpx.InternalError(w, err, "failed to write vault")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleLinkPut(w http.ResponseWriter, r *http.Request) {
	channel, ok := nameParam(w, r, "channel")
	if !ok {
		return
	}
	var p struct {
		Ciphertext string `json:"ciphertext"`
	}
	if !readCiphertext(w, r, &p) {
		return
	}
	if p.Ciphertext == "" || len(p.Ciphertext) > maxCiphertext {
		httpx.WriteError(w, http.StatusBadRequest, "ciphertext required")
		return
	}
	err := s.box.LinkPut(r.Context(), channel, p.Ciphertext)
	if errors.Is(err, ErrLinkUsed) {
		httpx.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		httpx.InternalError(w, err, "failed to answer link")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleLinkTake(w http.ResponseWriter, r *http.Request) {
	channel, ok := nameParam(w, r, "channel")
	if !ok {
		return
	}
	c, err := s.box.LinkTake(r.Context(), channel)
	if err != nil {
		httpx.InternalError(w, err, "failed to read link")
		return
	}
	if c == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"ciphertext": c})
}
