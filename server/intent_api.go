package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// controlIntent pauses, resumes, or cancels one local worker intent. A running
// worker is always stopped before state mutation/cleanup, preventing tool output
// that arrives after the user action from recreating deleted blackboard records.
func (s *Server) controlIntent(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task is being deleted; cannot control intents")
		return
	}
	defer s.engine.decInflight(t.ID)

	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || iid <= 0 {
		writeErr(w, 400, "bad intent id")
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"` // required for cancel (delete): the deletion reason
		Mode   string `json:"mode"`   // cancel only: soft (default, soft delete) | hard (real delete, cascading removal of exclusive descendants)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.Action != "pause" && req.Action != "resume" && req.Action != "cancel" {
		writeErr(w, 400, "action must be pause|resume|cancel")
		return
	}

	result, err := s.applyIntentControl(r.Context(), t, iid, req.Action, req.Reason, req.Mode)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

// rerunIntent reruns an intent that didn't succeed (blocked/exhausted/stopped): sets it back to open so a worker
// claims it again and runs it from scratch (facts/findings/assets already written to the graph are kept); revives the task if it is terminal/paused.
// Used for "continue running a failed work item": after network/LLM jitter causes blocked, retry with one click.
func restoreRerunIntent(t *Task, before *db.Node) error {
	if t == nil || before == nil {
		return fmt.Errorf("missing intent rollback snapshot")
	}
	if before.State == "blocked" && before.BlockedReason != "" {
		return t.Store.SetIntentBlockedReason(before.ID, before.BlockedReason)
	}
	return t.Store.SetIntentState(before.ID, before.State)
}

func (s *Server) rerunIntent(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad intent id")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task is being deleted; cannot rerun intents")
		return
	}
	defer s.engine.decInflight(t.ID)
	before, err := t.Store.GetNode(iid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	reopened, err := t.Store.ReopenIntent(iid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !reopened {
		writeErr(w, 409, "this intent is not in a rerunnable state (only blocked/exhausted/stopped can be rerun)")
		return
	}
	queued, err := s.admitTask(t, "resume")
	if err != nil {
		if rollbackErr := restoreRerunIntent(t, before); rollbackErr != nil {
			err = fmt.Errorf("%w; restore intent %d after admission failure: %v", err, iid, rollbackErr)
		}
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("[task] #%s intent #%d reopened (rerun)", t.ID, iid)
	writeJSON(w, 200, map[string]any{"id": t.ID, "reopened": iid, "queued": queued})
}

// rerunBlocked reruns all blocked intents of this task in bulk (for when one network/LLM disconnect leaves several blocked and
// you want to retry them all with one click), sets them back to open and revives the task; returns the number reopened.
func (s *Server) rerunBlocked(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task is being deleted; cannot rerun intents")
		return
	}
	defer s.engine.decInflight(t.ID)
	intents, err := t.Store.ListByKind(db.KindIntent, 1000000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	before := make([]*db.Node, 0)
	for _, intent := range intents {
		if intent.State == "blocked" {
			copy := *intent
			before = append(before, &copy)
		}
	}
	n, err := t.Store.ReopenBlockedIntents()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	queued := false
	if n > 0 {
		queued, err = s.admitTask(t, "resume")
		if err != nil {
			rollbackErrors := make([]string, 0)
			for _, intent := range before {
				if rollbackErr := restoreRerunIntent(t, intent); rollbackErr != nil {
					rollbackErrors = append(rollbackErrors, fmt.Sprintf("intent %d: %v", intent.ID, rollbackErr))
				}
			}
			if len(rollbackErrors) > 0 {
				err = fmt.Errorf("%w; restore blocked intents after admission failure: %s", err, strings.Join(rollbackErrors, "; "))
			}
			writeErr(w, 500, err.Error())
			return
		}
		log.Printf("[task] #%s reopened %d blocked intents in bulk", t.ID, n)
	}
	writeJSON(w, 200, map[string]any{"id": t.ID, "reopened": n, "queued": queued})
}

func (s *Server) frontier(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeJSON(w, 200, []any{})
		return
	}
	fr, _ := t.Store.Frontier(atoiDefault(r.URL.Query().Get("limit"), 100))
	writeJSON(w, 200, taskNodeDTOs(fr))
}

func (s *Server) intents(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		// Back-compat: bare list shape when the task can't be resolved.
		writeJSON(w, 200, []any{})
		return
	}
	q := r.URL.Query()
	limit := min(atoiDefault(q.Get("limit"), 300), 500)
	// No paging params → preserve the legacy bare-array response so existing callers
	// (and the poll) keep working unchanged.
	if q.Get("before") == "" && q.Get("page") == "" {
		in, err := t.Store.ListByKind(db.KindIntent, limit)
		if err != nil {
			log.Printf("[intents] task=%s limit=%d: %v", t.ID, limit, err)
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, taskNodeDTOs(in))
		return
	}
	// Paged form: ?before=<id> (or ?page as a marker) → {items, has_more} so the
	// worker session list can reach past the old fixed 300 boundary on scroll. It
	// also includes immutable intent results from directly related tasks; those
	// rows never participate in this task's frontier or claim path.
	before := int64(atoiDefault(q.Get("before"), 0))
	in, hasMore, err := taskIntentHistoryPage(t.Store, before, limit, false, 0)
	if err != nil {
		log.Printf("[intents] task=%s before=%d limit=%d: %v", t.ID, before, limit, err)
		writeErr(w, 500, err.Error())
		return
	}
	sources, err := t.Store.DirectSourceStores()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, source := range sources {
		items, sourceMore, sourceErr := taskIntentHistoryPage(source.Store, before, limit, true, source.Task.TaskID)
		if sourceErr != nil {
			writeErr(w, 500, sourceErr.Error())
			return
		}
		in = append(in, items...)
		hasMore = hasMore || sourceMore
	}
	sort.Slice(in, func(i, j int) bool { return in[i].ID > in[j].ID })
	if len(in) > limit {
		in = in[:limit]
		hasMore = true
	}
	writeJSON(w, 200, map[string]any{"items": taskNodeDTOs(in), "has_more": hasMore})
}
