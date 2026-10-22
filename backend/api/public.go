package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"digital-mirror/backend/auth"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/storage"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// queryInt reads a positive integer query parameter, def when absent or
// invalid, at most maxV.
func queryInt(r *http.Request, name string, def, maxV int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil && v > 0 {
		return min(v, maxV)
	}
	return def
}

// feedPage is a page of the feed; Next is the cursor of the next page, empty
// at the end.
type feedPage struct {
	Items []storage.FeedItem `json:"items"`
	Next  string             `json:"next,omitempty"`
}

// encodeFeedCursor writes the position after item as "<unix nanos>.<kind>.<id>".
func encodeFeedCursor(it storage.FeedItem) string {
	return fmt.Sprintf("%d.%s.%d", it.At.UnixNano(), it.Kind, it.ID)
}

func decodeFeedCursor(s string) (*storage.FeedCursor, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || (parts[1] != storage.FeedFact && parts[1] != storage.FeedContradiction) {
		return nil, errors.New("bad cursor")
	}
	ns, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, errors.New("bad cursor")
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, errors.New("bad cursor")
	}
	return &storage.FeedCursor{At: time.Unix(0, ns), Kind: parts[1], ID: id}, nil
}

// maxHotItems ends the hot feed: past it, the newest-first feed goes on.
const maxHotItems = 200

// handleFeed lists the contradictions and facts, newest first or in the hot
// order (?sort=new|hot), with the likes of the signed-in viewer marked
// (?kind=contradiction|fact, ?person=<id>, ?from=, ?to= the days of the
// statements, ?after=<cursor>, ?limit=). The cursor of the hot order is the
// offset of the next page.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != storage.FeedFact && kind != storage.FeedContradiction {
		writeError(w, http.StatusBadRequest, "kind is contradiction or fact")
		return
	}
	sort := r.URL.Query().Get("sort")
	if sort != "" && sort != "new" && sort != "hot" {
		writeError(w, http.StatusBadRequest, "sort is new or hot")
		return
	}
	f := storage.FeedFilter{Kind: kind, Hot: sort == "hot", ViewerID: s.viewerID(r)}
	var after *storage.FeedCursor
	if c := r.URL.Query().Get("after"); c != "" {
		var err error
		if f.Hot {
			f.Offset, err = strconv.Atoi(c)
			if err != nil || f.Offset < 0 || f.Offset >= maxHotItems {
				err = errors.New("bad cursor")
			}
		} else {
			after, err = decodeFeedCursor(c)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if v := r.URL.Query().Get("person"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "bad person")
			return
		}
		f.PersonID = id
	}
	// from and to are days (YYYY-MM-DD), both included.
	for _, d := range []struct {
		name string
		dst  **time.Time
		add  int
	}{{"from", &f.From, 0}, {"to", &f.To, 1}} {
		v := r.URL.Query().Get(d.name)
		if v == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			writeError(w, http.StatusBadRequest, d.name+" is a date (YYYY-MM-DD)")
			return
		}
		t = t.AddDate(0, 0, d.add)
		*d.dst = &t
	}
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		writeError(w, http.StatusBadRequest, "from is after to")
		return
	}
	limit := queryInt(r, "limit", 20, 50)
	items, err := s.storer.GetFeed(r.Context(), f, after, limit, publicMinFacts)
	if err != nil {
		httpInternalError(w, err, "failed to get feed")
		return
	}
	page := feedPage{Items: items}
	switch {
	case len(items) < limit:
	case f.Hot && f.Offset+limit < maxHotItems:
		page.Next = strconv.Itoa(f.Offset + limit)
	case !f.Hot:
		page.Next = encodeFeedCursor(items[len(items)-1])
	}
	writeJSON(w, http.StatusOK, page)
}

// viewerID is the signed-in user of a public request, 0 when signed out or
// when the session cannot be read (the page then shows no likes as theirs).
func (s *Server) viewerID(r *http.Request) int64 {
	token := bearerToken(r)
	if token == "" {
		return 0
	}
	user, err := s.storer.GetSessionUser(r.Context(), auth.HashToken(token))
	if err != nil {
		log.Warn().Err(err).Msg("failed to read the viewer's session")
		return 0
	}
	if user == nil {
		return 0
	}
	if _, ok := s.providers.Get(user.Provider); !ok {
		return 0
	}
	return user.ID
}

// likeState is the count of likes of a target, and whether the user gave one.
type likeState struct {
	Likes int  `json:"likes"`
	Liked bool `json:"liked"`
}

// handleLike gives (PUT) or takes back (DELETE) the signed-in user's +1 on a
// fact or a contradiction (/api/likes/{kind}/{id}).
func (s *Server) handleLike(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if kind != storage.FeedFact && kind != storage.FeedContradiction {
		writeError(w, http.StatusBadRequest, "kind is contradiction or fact")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	on := r.Method == http.MethodPut
	n, err := s.storer.SetLike(r.Context(), userFrom(r).ID, kind, id, on)
	if errors.Is(err, storage.ErrLikeTarget) {
		writeError(w, http.StatusNotFound, "no such item")
		return
	}
	if err != nil {
		httpInternalError(w, err, "failed to record like")
		return
	}
	action := "unlike"
	if on {
		action = "like"
	}
	metrics.FeedLikesTotal.WithLabelValues(kind, action).Inc()
	writeJSON(w, http.StatusOK, likeState{Likes: n, Liked: on})
}

// handleShare returns what a shared link points at, for its preview and to
// open the right stance (/api/share/{kind}/{id}?person=).
func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	personID, _ := strconv.ParseInt(r.URL.Query().Get("person"), 10, 64)
	t, err := s.storer.GetShareTarget(r.Context(), chi.URLParam(r, "kind"), id, personID)
	if errors.Is(err, storage.ErrNoShareTarget) {
		writeError(w, http.StatusNotFound, "no such item")
		return
	}
	if err != nil {
		httpInternalError(w, err, "failed to get share target")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleScoreExplanation returns every input of a person's score.
func (s *Server) handleScoreExplanation(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	e, err := s.storer.GetScoreExplanation(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such person")
		return
	}
	if err != nil {
		httpInternalError(w, err, "failed to explain score")
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// correctionsPage is a page of the public log of corrections.
type correctionsPage struct {
	Items []storage.Correction `json:"items"`
	Next  int64                `json:"next,omitempty"`
}

// handleCorrections lists the accepted reports (?before=<id>, ?limit=).
func (s *Server) handleCorrections(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit := queryInt(r, "limit", 20, 50)
	items, err := s.storer.ListCorrections(r.Context(), max(before, 0), limit)
	if err != nil {
		httpInternalError(w, err, "failed to list corrections")
		return
	}
	page := correctionsPage{Items: items}
	if len(items) == limit {
		page.Next = items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, page)
}

// codedError is a refusal with a code the page reads.
type codedError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// Codes of a refused report.
const (
	codeAlreadyReported = "already_reported"
	codeTooManyReports  = "too_many_reports"
	codeNoTarget        = "not_found"
	codeInvalidReport   = "invalid_report"
)

// Bounds of the reason of a report, in characters.
const (
	minReportReason = 10
	maxReportReason = 2000
)

// handleCreateReport records the signed-in user's report on a fact or a
// contradiction ({kind, target_id, reason}); the worker's judge answers it.
func (s *Server) handleCreateReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind     string `json:"kind"`
		TargetID int64  `json:"target_id"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, codedError{Error: "invalid body", Code: codeInvalidReport})
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Kind != storage.ReportKindFact && req.Kind != storage.ReportKindContradiction {
		writeJSON(w, http.StatusBadRequest, codedError{Error: "kind is fact or contradiction", Code: codeInvalidReport})
		return
	}
	if n := utf8.RuneCountInString(req.Reason); n < minReportReason || n > maxReportReason {
		writeJSON(w, http.StatusBadRequest, codedError{
			Error: fmt.Sprintf("the reason takes %d to %d characters", minReportReason, maxReportReason), Code: codeInvalidReport})
		return
	}
	user := userFrom(r)
	rep, err := s.storer.CreateUserReport(r.Context(), user.ID, req.Kind, req.TargetID, req.Reason)
	outcome := "created"
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, rep)
	case errors.Is(err, storage.ErrAlreadyReported):
		outcome = codeAlreadyReported
		writeJSON(w, http.StatusConflict, codedError{Error: err.Error(), Code: codeAlreadyReported})
	case errors.Is(err, storage.ErrTooManyReports):
		outcome = codeTooManyReports
		writeJSON(w, http.StatusTooManyRequests, codedError{Error: err.Error(), Code: codeTooManyReports})
	case errors.Is(err, storage.ErrReportTarget):
		outcome = codeNoTarget
		writeJSON(w, http.StatusNotFound, codedError{Error: err.Error(), Code: codeNoTarget})
	default:
		outcome = "error"
		httpInternalError(w, err, "failed to record report")
	}
	metrics.UserReportsTotal.WithLabelValues(req.Kind, outcome).Inc()
}

// myReports are the signed-in user's reports, and the targets they
// reported as "kind:id" keys.
type myReports struct {
	Reports  []storage.UserReport `json:"reports"`
	Reported []string             `json:"reported"`
}

func (s *Server) handleMyReports(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r)
	reports, err := s.storer.GetUserReports(r.Context(), user.ID)
	if err != nil {
		httpInternalError(w, err, "failed to list reports")
		return
	}
	keys := make([]string, 0, len(reports))
	for _, rep := range reports {
		keys = append(keys, rep.TargetKind+":"+strconv.FormatInt(rep.TargetID, 10))
	}
	writeJSON(w, http.StatusOK, myReports{Reports: reports, Reported: keys})
}
