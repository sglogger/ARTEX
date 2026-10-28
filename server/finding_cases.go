package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	actool "github.com/Autumn-27/norma/tool"
)

type findingCaseRequest struct {
	TaskID         json.RawMessage   `json:"task_id"`
	FindingID      json.RawMessage   `json:"finding_id"`
	IDs            []json.RawMessage `json:"finding_ids"`
	CaseID         json.RawMessage   `json:"case_id"`
	Version        int64             `json:"version"`
	Title          string            `json:"title"`
	Reason         string            `json:"reason"`
	Report         string            `json:"report"`
	Severity       string            `json:"severity"`
	SeverityReason string            `json:"severity_reason"`
	Page           int               `json:"page"`
}

func (a findingCaseRequest) ids() []int64 {
	out := []int64{}
	for _, raw := range a.IDs {
		out = append(out, parseProfileID(raw))
	}
	return out
}
func caseHTTPError(w http.ResponseWriter, err error) {
	code := 400
	if errors.Is(err, db.ErrFindingCaseConflict) {
		code = 409
	}
	writeErr(w, code, err.Error())
}
func decodeCaseRequest(w http.ResponseWriter, r *http.Request, a any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(a); err != nil {
		writeErr(w, 400, "invalid request body")
		return false
	}
	return true
}
func (s *Server) caseFindingDTO(f *db.DBFinding) FindingDTO {
	return findingFromDB(f, s.resolveAssetIDs(f.AssetIDs))
}
func (s *Server) listFindingCases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := findingPaginationParam(q.Get("page"), 1, 0)
	size := findingPaginationParam(q.Get("limit"), 20, 100)
	rows, total, err := s.m.pg.ListFindingCases(findingFilterFromQuery(q), page, size)
	if err != nil {
		caseHTTPError(w, err)
		return
	}
	items := []map[string]any{}
	for _, row := range rows {
		v := map[string]any{"case": row.Case, "matched_ids": row.MatchedIDs}
		if row.FindingID > 0 {
			f, err := s.m.pg.FindingCaseSummary(row.FindingID)
			if err != nil {
				caseHTTPError(w, err)
				return
			}
			if f == nil {
				continue
			}
			f.Report = ""
			v["finding"] = s.caseFindingDTO(f)
		}
		items = append(items, v)
	}
	stats, err := s.m.pg.DistinctFindingStats(q.Get("task_id"))
	if err != nil {
		caseHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": size, "stats": stats})
}
func (s *Server) findingCaseAccess(w http.ResponseWriter, r *http.Request, mutation bool) (*db.FindingCase, bool) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	c, err := s.m.pg.GetFindingCase(id)
	if err != nil {
		caseHTTPError(w, err)
		return nil, false
	}
	if c == nil {
		writeErr(w, 404, "finding case not found")
		return nil, false
	}
	if ctx := r.URL.Query().Get("context_task"); ctx != "" {
		t := s.m.ResolveTask(ctx)
		_, inherited, allowed := findingProvenanceInTask(t, c.TaskID)
		if !allowed {
			writeErr(w, 404, "the task has no such finding case")
			return nil, false
		}
		if mutation && inherited {
			writeErr(w, 403, "inherited finding cases are read-only")
			return nil, false
		}
	}
	return c, true
}
func (s *Server) getFindingCase(w http.ResponseWriter, r *http.Request) {
	c, ok := s.findingCaseAccess(w, r, false)
	if !ok {
		return
	}
	events, err := s.m.pg.FindingCaseEvents(c.ID)
	if err != nil {
		caseHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"case": c, "events": events})
}
func (s *Server) getFindingCaseMembers(w http.ResponseWriter, r *http.Request) {
	c, ok := s.findingCaseAccess(w, r, false)
	if !ok {
		return
	}
	page := findingPaginationParam(r.URL.Query().Get("page"), 1, 0)
	size := findingPaginationParam(r.URL.Query().Get("limit"), 20, 100)
	fs, total, err := s.m.pg.FindingCaseMembers(c.ID, page, size)
	if err != nil {
		caseHTTPError(w, err)
		return
	}
	assets := s.resolveFindingAssets(fs)
	items := []FindingDTO{}
	for _, f := range fs {
		dto := findingFromDB(f, assets)
		if ctx := r.URL.Query().Get("context_task"); ctx != "" {
			dto.SourceTaskID, dto.Inherited, _ = findingProvenanceInTask(s.m.ResolveTask(ctx), f.TaskID)
		}
		items = append(items, dto)
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) removeFindingCaseMember(w http.ResponseWriter, r *http.Request) {
	c, ok := s.findingCaseAccess(w, r, true)
	if !ok {
		return
	}
	fid := int64(atoiDefault(r.PathValue("fid"), 0))
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeCaseRequest(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Reason) == "" {
		body.Reason = "manually removed from the merged case"
	}
	if err := s.m.pg.RemoveFindingCaseMember(r.Context(), c.ID, fid, body.Reason); err != nil {
		caseHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) caseSuggestions(w http.ResponseWriter, r *http.Request) {
	items, err := s.m.pg.FindingCaseSuggestions(int64(atoiDefault(r.URL.Query().Get("task_id"), 0)))
	if err != nil {
		caseHTTPError(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (s *Server) resolveCaseSuggestion(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Accept bool `json:"accept"`
	}
	if !decodeCaseRequest(w, r, &a) {
		return
	}
	id, err := s.m.pg.ResolveFindingCaseSuggestion(r.Context(), int64(atoiDefault(r.PathValue("id"), 0)), a.Accept)
	if err != nil {
		caseHTTPError(w, err)
		return
	}
	result := map[string]any{"case_id": i64s(id), "report_queued": false}
	if a.Accept && id > 0 {
		// The correction is already committed. A disabled Reporter must not undo
		// that decision or make the client believe that the merge itself failed.
		runs, err := s.queueFindingCaseReport(id)
		if err != nil {
			result["report_error"] = err.Error()
		} else {
			result["report_queued"] = true
			result["runs"] = runs
		}
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) mergeFindingCase(w http.ResponseWriter, r *http.Request) {
	var a findingCaseRequest
	if !decodeCaseRequest(w, r, &a) {
		return
	}
	id, err := s.m.pg.MergeFindingCase(r.Context(), parseProfileID(a.TaskID), a.ids(), a.Title, a.Reason, "human")
	if err != nil {
		caseHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"case_id": i64s(id)})
}
func (s *Server) updateCaseReport(w http.ResponseWriter, r *http.Request) {
	c, ok := s.findingCaseAccess(w, r, true)
	if !ok {
		return
	}
	var a findingCaseRequest
	if !decodeCaseRequest(w, r, &a) {
		return
	}
	if err := s.m.pg.UpdateFindingCaseReport(r.Context(), c.ID, a.Version, a.Title, a.Report, a.Severity, a.SeverityReason); err != nil {
		caseHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) findingCaseTools() []actool.CoreTool {
	props := map[string]any{"finding_id": idParam("Standalone findings record ID, not the exploration node ID"), "finding_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "List of standalone finding record IDs"}, "task_id": idParam("ID of the shared source task"), "case_id": idParam("Finding case ID"), "version": idParam("The version actually read by get_finding_case"), "page": idParam("Page number, starting at 1, 20 per page"), "title": strParam("Name of the single underlying defect"), "reason": strParam("Concrete evidence and reason for merging; the same type alone is not enough"), "report": strParam("Complete unified Markdown report covering all members' evidence"), "severity": strParam("critical|high|medium|low"), "severity_reason": strParam("Rating rationale based on verified impact; do not simply take the highest level")}
	specs := []struct {
		name, desc string
		required   []string
	}{
		{"search_finding_duplicates", "Read-only search for candidates in the same task; returns summaries and assets. Findings with different severities may still be duplicates. Paginated; read details as needed.", []string{"finding_id"}},
		{"get_finding_record", "Read a standalone finding's complete raw evidence and report; returns the exploration node number so execution records can be traced.", []string{"finding_id"}},
		{"merge_finding_records", "Merge when the evidence clearly proves the same defect; all original records are kept. Does not cross tasks, does not change the original ratings, and cannot override a human veto.", []string{"task_id", "finding_ids", "title", "reason"}},
		{"suggest_finding_merge", "When evidence is insufficient, submit a suspected-duplicate suggestion for human confirmation; this is not an automatic merge. Only two records from the same task are allowed.", []string{"task_id", "finding_ids", "title", "reason"}},
		{"get_finding_case", "Read a finding case and a page of its members. Note the version first, then read the members' original reports, evidence and traffic; if the version changes you must read again.", []string{"case_id"}},
		{"update_finding_case_report", "Save the unified report and rating rationale; version must come from an actual read. Each original report is kept and no new finding is created.", []string{"case_id", "version", "title", "report", "severity", "severity_reason"}},
	}
	out := []actool.CoreTool{}
	for _, spec := range specs {
		spec := spec
		handler := func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a findingCaseRequest
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			v, err := s.performFindingCaseTool(ctx, spec.name, a)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			raw, _ := json.Marshal(v)
			return actool.Text(string(raw)), nil
		}
		schemaProps := map[string]any{}
		for _, key := range spec.required {
			schemaProps[key] = props[key]
		}
		if spec.name == "search_finding_duplicates" || spec.name == "get_finding_case" {
			schemaProps["page"] = props["page"]
		}
		schema := objSchema(schemaProps, spec.required...)
		if strings.HasPrefix(spec.name, "get_") || strings.HasPrefix(spec.name, "search_") {
			out = append(out, roTool(spec.name, spec.desc, schema, handler))
		} else {
			out = append(out, wrTool(spec.name, spec.desc, schema, handler))
		}
	}
	return out
}
func (s *Server) performFindingCaseTool(ctx context.Context, name string, a findingCaseRequest) (any, error) {
	if err := s.checkCaseReviewScope(ctx, name, a); err != nil {
		return nil, err
	}
	fid := parseProfileID(a.FindingID)
	cid := parseProfileID(a.CaseID)
	page := a.Page
	if page < 1 {
		page = 1
	}
	switch name {
	case "search_finding_duplicates":
		items, total, err := s.m.pg.FindingCaseCandidates(fid, page, 20)
		return map[string]any{"items": items, "total": total, "page": page, "page_size": 20}, err
	case "get_finding_record":
		f, err := s.m.pg.GetFinding(fid)
		if err != nil {
			return nil, err
		}
		if f == nil {
			return nil, errors.New("finding not found")
		}
		dto := s.caseFindingDTO(f)
		cid, err := s.m.pg.FindingCaseID(f.ID)
		if err != nil {
			return nil, err
		}
		if cid > 0 {
			dto.CaseID = i64s(cid)
		}
		return map[string]any{"finding": dto, "finding_node_id": f.NodeID}, nil
	case "merge_finding_records":
		id, err := s.m.pg.MergeFindingCase(ctx, parseProfileID(a.TaskID), a.ids(), a.Title, a.Reason, "reporter")
		return map[string]string{"case_id": i64s(id)}, err
	case "suggest_finding_merge":
		ids := a.ids()
		if len(ids) != 2 {
			return nil, errors.New("a suspected-duplicate suggestion needs two records")
		}
		id, err := s.m.pg.SuggestFindingCase(ctx, parseProfileID(a.TaskID), ids[0], ids[1], a.Title, a.Reason)
		return map[string]string{"suggestion_id": i64s(id)}, err
	case "get_finding_case":
		c, err := s.m.pg.GetFindingCase(cid)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, errors.New("finding case not found")
		}
		fs, total, err := s.m.pg.FindingCaseMembers(cid, page, 20)
		members := []FindingDTO{}
		for _, f := range fs {
			members = append(members, s.caseFindingDTO(f))
		}
		return map[string]any{"case": c, "members": members, "total": total, "page": page, "page_size": 20}, err
	case "update_finding_case_report":
		err := s.m.pg.UpdateFindingCaseReport(ctx, cid, a.Version, a.Title, a.Report, a.Severity, a.SeverityReason)
		return map[string]bool{"ok": err == nil}, err
	}
	return nil, errors.New("unknown merge tool")
}
func idParam(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func (s *Server) seedFindingCaseTools() {
	_, _ = s.m.pg.Exec(`UPDATE finding_case_review_runs SET state='failed',error='interrupted by a service restart, please submit the review again' WHERE state IN ('queued','running')`)
	a, err := s.m.pg.GetAgentByKey("reporter")
	if err != nil || a == nil {
		return
	}
	const flag = "finding_case_tools_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	names := []string{}
	for _, t := range s.findingCaseTools() {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.pg.SeedTool(t.Name(), t.Description(), schema, json.RawMessage(`["reporter"]`)); err != nil {
			return
		}
		names = append(names, t.Name())
	}
	if err := s.m.pg.AddAgentToToolBinding("reporter", names); err != nil {
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}
func (s *Server) reviewFindingCases(w http.ResponseWriter, r *http.Request) {
	var a findingCaseRequest
	if !decodeCaseRequest(w, r, &a) {
		return
	}
	ids := a.ids()
	if cid := parseProfileID(a.CaseID); cid > 0 {
		c, err := s.m.pg.GetFindingCase(cid)
		if err != nil || c == nil || !c.Active {
			writeErr(w, 400, "finding case unavailable")
			return
		}
		if ctx := r.URL.Query().Get("context_task"); ctx != "" && (c.TaskID == nil || ctx != i64s(*c.TaskID)) {
			writeErr(w, 403, "inherited finding cases are read-only")
			return
		}
		fs, _, err := s.m.pg.FindingCaseMembers(cid, 1, 100000)
		if err != nil {
			caseHTTPError(w, err)
			return
		}
		ids = nil
		for _, f := range fs {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) == 0 || (len(a.CaseID) == 0 && len(ids) > 200) {
		writeErr(w, 400, "select 1 to 200 original reports")
		return
	}
	runs, err := s.queueFindingCaseReview(ids)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"runs": runs})
}

// queueFindingCaseReview reuses the Reporter queue for both selected reviews and
// optional human corrections. Neither path introduces an approval gate.
func (s *Server) queueFindingCaseReview(ids []int64) ([]map[string]any, error) {
	reporter, err := s.m.pg.GetAgentByKey("reporter")
	if err != nil {
		return nil, err
	}
	if reporter == nil || !reporter.Enabled || s.chatAgentRef() == nil {
		return nil, errors.New("report agent is not enabled or the model is unavailable")
	}
	batches := map[int64][]int64{}
	seen := map[int64]bool{}
	for _, fid := range ids {
		if seen[fid] {
			continue
		}
		seen[fid] = true
		f, err := s.m.pg.FindingCaseSummary(fid)
		if err != nil {
			return nil, err
		}
		if f == nil || f.TaskID == nil {
			return nil, errors.New("source task of the selected findings is unavailable")
		}
		batches[*f.TaskID] = append(batches[*f.TaskID], fid)
	}
	// Validate all sources before scheduling any work.
	for taskID := range batches {
		if _, ok := s.m.Task(i64s(taskID)); !ok {
			return nil, errors.New("task unavailable")
		}
	}
	runs := []map[string]any{}
	for taskID, fids := range batches {
		t, ok := s.m.Task(i64s(taskID))
		if !ok {
			return nil, errors.New("task unavailable")
		}
		raw, err := json.Marshal(fids)
		if err != nil {
			return nil, err
		}
		msg := fmt.Sprintf("Selected report review: only process the selected finding_ids=%s of this task. First keep/generate each standalone report, then judge within the selection whether they are the same defect; if clear, merge automatically and update the unified report. If unsure, keep the standalone reports; you may submit a suspected-duplicate suggestion for later review, but you must not wait for human confirmation or block report generation. If already grouped, read all members and their actual versions before generating the unified report. Do not merge unselected records and do not probe targets.", raw)
		c, err := s.m.pg.CreateConversation("reporter", "Finding review · task#"+t.ID, nil)
		if err != nil {
			return nil, err
		}
		if _, err := s.m.pg.Exec(`INSERT INTO finding_case_review_runs(conversation_id,task_id,finding_ids) VALUES($1,$2,$3)`, c.ID, taskID, string(raw)); err != nil {
			return nil, err
		}
		item := triggeredRun{agentKey: "reporter", title: c.Title, message: msg, taskID: taskID, taskDesc: t.descriptionText(), taskGoal: t.goalText(), conversationID: c.ID}
		cfg := s.readTriggerBehavior("reporter")
		s.queueMu.Lock()
		s.triggerCfg["reporter"] = cfg
		s.triggerQ["reporter"] = append(s.triggerQ["reporter"], item)
		s.pumpLocked("reporter")
		s.queueMu.Unlock()
		runs = append(runs, map[string]any{"conversation_id": c.ID, "task_id": i64s(taskID)})
	}
	return runs, nil
}

// A correction expands to all existing members so the refreshed report retains
// the complete case context without allowing unrelated findings to be changed.
func (s *Server) queueFindingCaseReport(id int64) ([]map[string]any, error) {
	members, _, err := s.m.pg.FindingCaseMembers(id, 1, 100000)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.ID)
	}
	if len(ids) == 0 {
		return nil, errors.New("finding case has no usable members")
	}
	return s.queueFindingCaseReview(ids)
}

func (s *Server) caseReviewRuns(w http.ResponseWriter, r *http.Request) {
	rows, err := s.m.pg.Query(`SELECT conversation_id,task_id,state,error FROM finding_case_review_runs ORDER BY conversation_id DESC LIMIT 50`)
	if err != nil {
		caseHTTPError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var conv int64
		var task sql.NullInt64
		var state, msg string
		if err := rows.Scan(&conv, &task, &state, &msg); err != nil {
			caseHTTPError(w, err)
			return
		}
		items = append(items, map[string]any{"conversation_id": conv, "task_id": i64s(task.Int64), "state": state, "error": msg})
	}
	if err := rows.Err(); err != nil {
		caseHTTPError(w, err)
		return
	}
	writeJSON(w, 200, items)
}

// Manual reviews cannot mutate a finding outside the explicit selection.
func (s *Server) checkCaseReviewScope(ctx context.Context, name string, a findingCaseRequest) error {
	conv := intercept.ConvIDFromContext(ctx)
	if conv == 0 {
		return nil
	}
	var raw json.RawMessage
	err := s.m.pg.QueryRow(`SELECT finding_ids FROM finding_case_review_runs WHERE conversation_id=$1`, conv).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var ids []int64
	if err := json.Unmarshal(raw, &ids); err != nil {
		return err
	}
	allowed := map[int64]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	switch name {
	case "merge_finding_records", "suggest_finding_merge", "update_finding_report", "update_finding_evidence":
		checkIDs := a.ids()
		if name == "update_finding_report" || name == "update_finding_evidence" {
			checkIDs = []int64{parseProfileID(a.FindingID)}
		}
		for _, id := range checkIDs {
			if !allowed[id] {
				return errors.New("manual review can only merge the selected records")
			}
		}
	case "update_finding_case_report":
		fs, _, err := s.m.pg.FindingCaseMembers(parseProfileID(a.CaseID), 1, 100000)
		if err != nil {
			return err
		}
		found := false
		for _, f := range fs {
			if allowed[f.ID] {
				found = true
			}
		}
		if !found {
			return errors.New("this finding case is outside the manually selected scope")
		}
	}
	return nil
}
