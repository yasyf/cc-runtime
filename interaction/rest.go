package interaction

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"

	"github.com/yasyf/cc-interact/daemon"
)

// maxAnswerBytes caps the POST answer body so an oversized payload can't be
// decoded into memory or persisted to the event log.
const maxAnswerBytes = 256 << 10

type sessionsReply struct {
	Subjects []ListedSubject `json:"subjects"`
}

// restServer holds the REST plane's shared state: the projection connection
// and the Append chokepoint the answer route writes through.
type restServer struct {
	server *daemon.Server
	append daemon.AppendFunc
}

// MountREST mounts the interaction ops' HTTP surface — sessions (the list op
// across scopes), pending, and answer — on the daemon's mux, which startHTTP
// wraps whole in its auth handler, so the loopback bypass and bearer-token
// semantics apply to every route unchanged.
func MountREST(s *daemon.Server) {
	rs := &restServer{server: s, append: s.Append}
	mux := s.Mux()
	mux.HandleFunc("GET /api/sessions", rs.handleSessions)
	mux.HandleFunc("GET /api/subjects/{id}/pending", rs.handlePending)
	mux.HandleFunc("POST /api/subjects/{id}/answer", rs.handleAnswer)
}

// handleSessions lists every active subject with its open-question count, so a
// web client — which holds no scope — can pick one to open.
func (rs *restServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	subjects, err := listSessions(r.Context(), rs.server.DB())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessionsReply{Subjects: subjects})
}

// handlePending lists a subject's open questions with their full payloads,
// 404ing an unknown subject rather than answering it with an empty set.
func (rs *restServer) handlePending(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	known, err := subjectExists(r.Context(), rs.server.DB(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !known {
		http.Error(w, "unknown subject: "+id, http.StatusNotFound)
		return
	}
	questions, err := openQuestions(r.Context(), rs.server.DB(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, pendingReply{Questions: questions})
}

// RequireJSON rejects a request whose Content-Type is not application/json
// with 415, reporting whether the caller may proceed. Every state-changing
// JSON route sits behind it as CSRF hardening: the daemon's auth layer admits
// tokenless loopback and trusted-peer requests under a localhost Origin, and a
// hostile page on such a machine can fire preflight-free "simple" POSTs
// (text/plain, form encodings) — but it cannot send application/json without a
// CORS preflight the daemon never answers.
func RequireJSON(w http.ResponseWriter, r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

// handleAnswer maps POST /api/subjects/{id}/answer onto the socket answer op's
// core: same idempotent dedup, same append-first gate release. The path names
// the subject; a body subject_id may only restate it. Unknown targets are 404,
// malformed bodies 400 — never a silent success.
func (rs *restServer) handleAnswer(w http.ResponseWriter, r *http.Request) {
	if !RequireJSON(w, r) {
		return
	}
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, maxAnswerBytes)
	var a AnswerPayload
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, fmt.Sprintf("answer exceeds %d bytes", maxAnswerBytes), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad answer body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if a.SubjectID != "" && a.SubjectID != id {
		http.Error(w, fmt.Sprintf("body subject_id %q disagrees with path subject %q", a.SubjectID, id), http.StatusBadRequest)
		return
	}
	a.SubjectID = id
	idled, err := applyAnswer(r.Context(), rs.server.DB(), rs.append, a)
	var unknown unknownQuestionError
	switch {
	case errors.As(err, &unknown):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, answerReply{Idled: idled})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
