package server

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/Autumn-27/artex/agent"
	actool "github.com/Autumn-27/norma/tool"
)

func (s *Server) toolUpdateFindingEvidence() actool.CoreTool {
	return wrTool("update_finding_evidence", "Supplement the complete evidence and source of an existing finding, overwriting it entirely while keeping an audit of the old evidence. finding_id is the standalone finding record ID; read get_finding_record/get_finding_traffic first to get the latest evidence_version. The source fact must already be saved; references or the presence of traffic do not replace proof that the mechanism holds. Supplementing evidence invalidates the old review and report; do not record the finding again.",
		objSchema(map[string]any{
			"finding_id":       strParam("Standalone finding record ID, not the exploration node ID"),
			"evidence_version": map[string]any{"type": "integer", "minimum": 0},
			"evidence":         strParam("The complete actual operations, key request/response content, mechanism conditions, observations and limitations; don't write only hashes or existing record numbers"),
			"reason":           strParam("Reason for the supplement or correction"),
			"source_fact_ids":  map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "integer"}},
		}, "finding_id", "evidence_version", "evidence", "reason"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				FindingID json.RawMessage `json:"finding_id"`
				Version   *int64          `json:"evidence_version"`
				Evidence  string          `json:"evidence"`
				Reason    string          `json:"reason"`
				Facts     []int64         `json:"source_fact_ids"`
			}
			if err := json.Unmarshal(in, &a); err != nil || a.Version == nil || strings.TrimSpace(a.Reason) == "" {
				return actool.Errorf("finding_id, evidence_version, the complete evidence and reason are required"), nil
			}
			id := parseProfileID(a.FindingID)
			if err := s.agentFindingTrafficAccess(ctx, id, true); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := s.checkCaseReviewScope(ctx, "update_finding_evidence", findingCaseRequest{FindingID: a.FindingID}); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			ri := agent.RunInfoFrom(ctx)
			actor := ri.SessionID
			if actor == "" {
				actor = "agent"
			}
			version, err := s.m.pg.UpdateFindingEvidence(ctx, id, *a.Version, ri.TaskID, a.Evidence, actor, a.Reason, a.Facts)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"finding_id": i64s(id), "evidence_version": version, "note": "evidence updated; the old review and report need to be reviewed again"})
		})
}

func (s *Server) seedEvidenceHandoffBindings() {
	const flag = "check_evidence_handoff_v1"
	if value, _, _ := s.m.pg.GetSetting(flag); value == "true" {
		return
	}
	for _, role := range []string{"worker", "planner", "reporter"} {
		if err := s.m.pg.AddAgentToToolBinding(role, []string{"get_finding_record", "get_finding_traffic", "bind_finding_traffic", "update_finding_evidence", "update_finding_report"}); err != nil {
			log.Printf("[evidence] handoff binding: %v", err)
			return
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
}
