package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// Manual CRUD endpoints for "goal management" in the overview. They write the same goal nodes as the agent-side set_goals tool,
// but the entry point is a human adding/editing/deleting in the UI; after add/edit they reuse the "revive task" logic (admitTask resume:
// terminal→running, unpause, queue if needed); delete does not revive (product decision). Every mutating handler goes through
// beginTaskOperation/decInflight to avoid racing with task deletion (same as intent CRUD).

// listGoals returns all goals of this task (text/vulnclass/state split out) for rendering the goal management card.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	goals, err := t.Store.ListByKind(db.KindGoal, 10000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"goals": goalDTOs(goals)})
}

func (s *Server) reopenGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task is being deleted; cannot correct goals")
		return
	}
	defer s.engine.decInflight(t.ID)
	goalID, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || goalID <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var input struct {
		Version *int   `json:"expected_version"`
		Reason  string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Version == nil || *input.Version < 0 || strings.TrimSpace(input.Reason) == "" {
		writeErr(w, 400, "goal version and correction reason are required")
		return
	}
	if err = t.Store.ReopenGoal(r.Context(), goalID, *input.Version, "human", strings.TrimSpace(input.Reason)); err != nil {
		if errors.Is(err, db.ErrGoalProofChanged) {
			writeErr(w, 409, err.Error())
		} else {
			writeErr(w, 500, err.Error())
		}
		return
	}
	t.NotifyGoal([]string{"original completion verdict corrected: " + input.Reason})
	s.reviveTask(t)
	node, err := t.Store.GetNode(goalID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// addGoal manually adds a goal: persist (under the task root's spawns) → record a "goal added" trigger to wake
// the planner → revive the task, so the planner re-judges completion against the new goal.
func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task is being deleted; cannot add goals")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body db.EngagementGoal
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if err := body.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	payload := map[string]any{"text": text, "goal_type": body.GoalType, "required_checks": body.RequiredChecks}
	if vc := strings.TrimSpace(body.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	id, err := t.Store.AddGoal(payload, "human")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if of, _ := t.Store.OriginFactID(); of > 0 && id > 0 {
		_ = t.Store.Link(of, db.RelSpawns, id) // goal descends from the task root (origin fact)
	}
	t.NotifyGoal([]string{text}) // records "the human added goals: ..." trigger and wakes the planner
	s.reviveTask(t)              // bring a completed/paused task back into the running state
	node, _ := t.Store.GetNode(id)
	if node == nil {
		writeErr(w, 500, "failed to read goal after writing")
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// editGoal manually edits a goal's text (and vulnclass): update the DB → record "the user changed the goal from old to new"
// trigger to wake the planner → revive the task, so the planner adjusts direction to the new goal.
func (s *Server) editGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task is being deleted; cannot edit goals")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "goal content must not be empty")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "goal not found")
		return
	}
	oldText := goalDTO(node).Text
	if err := t.Store.UpdateGoalPayload(gid, text, strings.TrimSpace(body.VulnClass)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalEdited(oldText, text) // records "the human changed the goal from old to new" trigger and wakes the planner
	s.reviveTask(t)                   // same as add: revive the task to re-judge against the new goal
	updated, _ := t.Store.GetNode(gid)
	if updated == nil {
		writeErr(w, 500, "failed to read goal after updating")
		return
	}
	writeJSON(w, 200, goalDTO(updated))
}

// deleteGoal manually deletes a goal (hard delete, cascading edges/anchors): delete from the DB → record "the user deleted goal X"
// trigger to wake the planner to re-judge the remaining goals. Per product decision, deletion does [not] revive the task.
func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task is being deleted; cannot delete goals")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "goal not found")
		return
	}
	text := goalDTO(node).Text
	if err := t.Store.DeleteGoal(gid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalDeleted(text) // records "the human deleted goal: ..." trigger and wakes the planner (no task revive)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
