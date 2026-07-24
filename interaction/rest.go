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

type restServer struct {
	server *daemon.Server
}

// MountREST mounts the interaction ops' HTTP surface — sessions (the list op
// across scopes), pending, and answer — on the daemon's mux, which startHTTP
// wraps whole in its auth handler, so the loopback bypass and bearer-token
// semantics apply to every route unchanged.
func MountREST(s *daemon.Server) {
	rs := &restServer{server: s}
	mux := s.Mux()
	mux.HandleFunc("GET /api/sessions", rs.handleSessions)
	mux.HandleFunc("GET /api/subjects/{id}/pending", rs.handlePending)
	mux.HandleFunc("POST /api/subjects/{id}/answer", rs.handleAnswer)
}

// handleSessions lists every active subject with its open-question count, so a
// web client — which holds no scope — can pick one to open.
func (rs *restServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	reply := rs.server.Dispatch(r.Context(), daemon.Envelope{Op: OpSessions})
	if !reply.OK {
		http.Error(w, reply.Error, http.StatusInternalServerError)
		return
	}
	var listed listReply
	if err := json.Unmarshal(reply.Body, &listed); err != nil {
		http.Error(w, "decode sessions reply: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessionsReply{Subjects: listed.Subjects})
}

// handlePending lists a subject's open questions with their full payloads,
// 404ing an unknown subject rather than answering it with an empty set.
func (rs *restServer) handlePending(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, err := json.Marshal(subjectBody{SubjectID: id})
	if err != nil {
		http.Error(w, "encode pending request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	reply := rs.server.Dispatch(r.Context(), daemon.Envelope{Op: OpPending, Body: body})
	if !reply.OK {
		http.Error(w, reply.Error, http.StatusInternalServerError)
		return
	}
	var pending pendingReply
	if err := json.Unmarshal(reply.Body, &pending); err != nil {
		http.Error(w, "decode pending reply: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if pending.Missing {
		http.Error(w, "unknown subject: "+id, http.StatusNotFound)
		return
	}
	writeJSON(w, pendingReply{Questions: pending.Questions})
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
	body, err := json.Marshal(a)
	if err != nil {
		http.Error(w, "encode answer request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	reply := rs.server.Dispatch(r.Context(), daemon.Envelope{Op: OpAnswer, Body: body})
	var answer answerReply
	if len(reply.Body) > 0 {
		if err := json.Unmarshal(reply.Body, &answer); err != nil {
			http.Error(w, "decode answer reply: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if !reply.OK {
		if answer.Missing {
			http.Error(w, reply.Error, http.StatusNotFound)
			return
		}
		http.Error(w, reply.Error, http.StatusInternalServerError)
		return
	}
	writeJSON(w, answerReply{Idled: answer.Idled})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
