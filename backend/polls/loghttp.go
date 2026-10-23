package polls

import (
	"errors"
	"net/http"
	"strconv"

	"digital-mirror/backend/httpx"
)

// handleCheckpoint returns the signed checkpoint of the log's head
// (text/plain, a signed note).
func (s *Service) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	head, err := s.box.Head(r.Context())
	if err != nil {
		httpx.InternalError(w, err, "failed to read the vote log")
		return
	}
	msg, err := s.signer.Sign(head)
	if err != nil {
		httpx.InternalError(w, err, "failed to sign the checkpoint")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(msg)
}

// handleLogKey returns the verifier key of the checkpoints.
func (s *Service) handleLogKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(s.signer.VerifierKey() + "\n"))
}

// proofResponse is a proof: the tree hashes, in base64.
type proofResponse struct {
	Hashes [][]byte `json:"hashes"`
}

// handleProveEntry proves that the tree of ?size entries holds entry ?seq.
func (s *Service) handleProveEntry(w http.ResponseWriter, r *http.Request) {
	size, err1 := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
	seq, err2 := strconv.ParseInt(r.URL.Query().Get("seq"), 10, 64)
	if err1 != nil || err2 != nil {
		httpx.WriteError(w, http.StatusBadRequest, "size and seq are entry counts")
		return
	}
	p, err := s.box.ProveEntry(r.Context(), size, seq)
	writeProof(w, p, err)
}

// handleProveAppend proves that the tree of ?size entries extends the tree
// of ?old entries.
func (s *Service) handleProveAppend(w http.ResponseWriter, r *http.Request) {
	size, err1 := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
	old, err2 := strconv.ParseInt(r.URL.Query().Get("old"), 10, 64)
	if err1 != nil || err2 != nil {
		httpx.WriteError(w, http.StatusBadRequest, "size and old are entry counts")
		return
	}
	p, err := s.box.ProveAppend(r.Context(), size, old)
	writeProof(w, p, err)
}

func writeProof[H ~[32]byte](w http.ResponseWriter, p []H, err error) {
	if errors.Is(err, ErrBadProofRequest) {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		httpx.InternalError(w, err, "failed to prove")
		return
	}
	out := proofResponse{Hashes: make([][]byte, len(p))}
	for i, h := range p {
		out.Hashes[i] = append([]byte(nil), h[:]...)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleStamps lists the checkpoints stamped on Bitcoin with their .ots
// proofs (base64).
func (s *Service) handleStamps(w http.ResponseWriter, r *http.Request) {
	stamps, err := s.box.Stamps(r.Context())
	if err != nil {
		httpx.InternalError(w, err, "failed to read the stamps")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, stamps)
}
