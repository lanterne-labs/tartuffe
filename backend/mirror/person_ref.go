package mirror

import (
	"context"
	"net/http"
	"regexp"
	"strconv"

	"digital-mirror/backend/httpx"

	"github.com/go-chi/chi/v5"
)

// A person in a URL is their id or their slug: /api/person/42 and
// /api/person/marine-le-pen name the same person. The site links by slug
// (persons.slug, set from the name at the insert and never changed); the
// ids stay for the links already shared.
var slugShape = regexp.MustCompile(`^[a-z0-9-]+$`)

// resolvePerson gives the id of the person ref names, by id or by slug; 0
// when ref is empty or no person has the slug (an id is taken as given).
func (s *Service) resolvePerson(ctx context.Context, ref string) (int64, error) {
	if ref == "" {
		return 0, nil
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return id, nil
	}
	if !slugShape.MatchString(ref) {
		return 0, nil
	}
	return s.store.GetPersonIDBySlug(ctx, ref)
}

// personID resolves the {id} of the route; when it names nobody, the
// answer is written (400 for a malformed reference, 404 for an unknown
// slug) and ok is false.
func (s *Service) personID(w http.ResponseWriter, r *http.Request) (id int64, ok bool) {
	ref := chi.URLParam(r, "id")
	if _, err := strconv.ParseInt(ref, 10, 64); err != nil && !slugShape.MatchString(ref) {
		httpx.WriteError(w, http.StatusBadRequest, "Invalid person ID")
		return 0, false
	}
	id, err := s.resolvePerson(r.Context(), ref)
	if err != nil {
		httpx.InternalError(w, err, "failed to resolve person")
		return 0, false
	}
	if id == 0 {
		httpx.WriteError(w, http.StatusNotFound, "Person not found")
		return 0, false
	}
	return id, true
}
