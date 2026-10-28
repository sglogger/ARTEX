package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/db"
)

func (s *Server) updateEngagement(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task is being deleted")
		return
	}
	defer s.engine.decInflight(t.ID)
	var change db.EngagementChange
	if json.NewDecoder(r.Body).Decode(&change) != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	s.concMu.Lock()
	defer s.concMu.Unlock()
	if !evidenceEditQuiescent(s, t) {
		writeErr(w, 409, "pause the task and wait for work to exit before updating the scope, goals and constraints")
		return
	}
	id, _ := strconv.ParseInt(t.ID, 10, 64)
	if err := s.m.pg.UpdateEngagement(r.Context(), id, change); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	t.setContract(change.Goal, change.Description)
	t.NotifyGoalEdited("task contract", change.Reason)
	writeJSON(w, 200, map[string]any{"ok": true, "paused": true, "note": "Goals, constraints and scope saved together; the old contract audit is kept, and after resuming execution and review follow the new contract."})
}

func evidenceEditQuiescent(s *Server, t *Task) bool {
	// This HTTP edit owns one operation admission; all other execution must leave.
	return s.engine.IsPaused(t.ID) && s.engine.inflightCount(t.ID) <= 1
}
