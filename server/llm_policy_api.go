package server

import (
	"net/http"

	"github.com/Autumn-27/artex/db"
)

// pgGetLLMRetryPolicy returns the global retry policy (count + interval for each of the five layers). Never configured → all zeros,
// which the frontend shows as "default".
func (s *Server) pgGetLLMRetryPolicy(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	writeJSON(w, 200, pg.LLMRetryPolicy())
}

// pgSaveLLMRetryPolicy saves the global retry policy. The three layers that "follow the endpoint" (connect/empty response/same-
// provider safe window) are provider build or call parameters, so after a change the cached providers must be
// rebuilt; circuit breaker parameters are pushed straight to the process-level Registry.
func (s *Server) pgSaveLLMRetryPolicy(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var pol db.LLMRetryPolicy
	if err := decode(r, &pol); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.SetLLMRetryPolicy(pol); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.applyRetryPolicy()
	s.invalidateProfileAgents()
	s.reapplyActiveProfile()
	writeJSON(w, 200, pg.LLMRetryPolicy())
}

// pgLLMPoolStatus reports the failover ("rotation") switches, the resolved chain order
// and every profile's circuit-breaker state — what the LLM page renders as the
// "rotation order" strip and the per-card health badges.
func (s *Server) pgLLMPoolStatus(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	writeJSON(w, 200, s.llmPoolStatus())
}

// pgLLMPoolReset clears a tripped profile's circuit breaker so the next call
// tries it again immediately ("recover now"). id=0 clears every profile.
func (s *Server) pgLLMPoolReset(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		ID int64 `json:"id"` // 0 / omitted = all
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if body.ID > 0 {
		s.llmHealth.Reset(body.ID)
	} else {
		for id := range s.llmHealth.Snapshot() {
			s.llmHealth.Reset(id)
		}
	}
	writeJSON(w, 200, s.llmPoolStatus())
}
