package agent

import (
	"encoding/json"
	"fmt"
)

const completionReviewByteLimit = 32 << 10

type completionReviewTask struct {
	ID          int64  `json:"id"`
	Goal        string `json:"goal"`
	Description string `json:"description"`
}

type completionReviewReference struct {
	NodeID   int64 `json:"node_id"`
	GoalID   int64 `json:"goal_id,omitempty"`
	IntentID int64 `json:"intent_id,omitempty"`
}

type completionReviewSummary struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Summary string `json:"summary,omitempty"`
}

type completionReviewInput struct {
	Task               completionReviewTask        `json:"task"`
	Goals              []json.RawMessage           `json:"goals"`
	Constraints        json.RawMessage             `json:"constraints"`
	RemainingWork      json.RawMessage             `json:"remaining_work"`
	GoalsMet           bool                        `json:"goals_met"`
	Eligible           bool                        `json:"eligible"`
	TimedOut           bool                        `json:"timed_out,omitempty"`
	EvidenceReferences []completionReviewReference `json:"evidence_references"`
	EvidenceSummaries  []completionReviewSummary   `json:"evidence_summaries"`
	EvidenceTotal      int                         `json:"evidence_total"`
	EvidenceOmitted    int                         `json:"evidence_omitted"`
	Notice             string                      `json:"notice"`
}

// completionReviewLimit uses a conservative one-byte-per-token allowance for
// this initial input, reserving at least half the window for instructions, tool
// definitions/results and the verdict. This is a byte cap, not a token estimate.
func (p *Planner) completionReviewLimit() int {
	limit := completionReviewByteLimit
	if window := p.compactionWindow(); window > 0 && window/2 < limit {
		limit = window / 2
	}
	return limit
}

// buildReviewInput preserves all decision-critical inputs. Only supporting
// evidence summaries are sampled; their IDs can be inspected with read-only
// tools. The durable snapshot and its commit digest remain untouched.
func buildReviewInput(snapshot json.RawMessage, taskID, expID int64, limit int) (json.RawMessage, error) {
	var input struct {
		Work     json.RawMessage `json:"remaining_work"`
		GoalsMet bool            `json:"goals_met"`
		Eligible bool            `json:"eligible"`
		TimedOut bool            `json:"timed_out"`
		Data     struct {
			Tasks       []completionReviewTask `json:"tasks"`
			Nodes       []json.RawMessage      `json:"nodes"`
			Constraints json.RawMessage        `json:"constraints"`
			Edges       []struct {
				Source int64  `json:"src_id"`
				Target int64  `json:"dst_id"`
				Rel    string `json:"rel"`
			} `json:"edges"`
		} `json:"data"`
	}
	if err := json.Unmarshal(snapshot, &input); err != nil {
		return nil, fmt.Errorf("parse final verification snapshot: %w", err)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("final verification input budget is insufficient; goals or remaining checks cannot be omitted")
	}
	out := completionReviewInput{
		Goals: []json.RawMessage{}, Constraints: input.Data.Constraints, RemainingWork: input.Work,
		GoalsMet: input.GoalsMet, Eligible: input.Eligible, TimedOut: input.TimedOut,
		EvidenceReferences: []completionReviewReference{}, EvidenceSummaries: []completionReviewSummary{},
		Notice: "The task's original goal, description, all current goal nodes, operating constraints and remaining checks are kept in full. Evidence summaries are only sampled, and long summaries have been shortened; assets and other graph data are not attached. Do not treat evidence that isn't shown as nonexistent. Use node_detail, list_facts, list_findings, get_worker_output and get_worker_trace to look up the saved evidence referenced; when it cannot be verified sufficiently, you must refuse completion.",
	}
	if len(out.RemainingWork) == 0 || string(out.RemainingWork) == "null" {
		out.RemainingWork = json.RawMessage(`[]`)
	}
	if len(out.Constraints) == 0 || string(out.Constraints) == "null" {
		out.Constraints = json.RawMessage(`[]`)
	}
	foundTask := false
	for _, task := range input.Data.Tasks {
		if task.ID == taskID {
			out.Task, foundTask = task, true
			break
		}
	}
	if !foundTask {
		return nil, fmt.Errorf("final verification snapshot is missing the original goal and description of the current task %d", taskID)
	}
	goalIDs := map[int64]bool{}
	var summaries []completionReviewSummary
	for _, raw := range input.Data.Nodes {
		var node struct {
			ID            int64           `json:"id"`
			ExplorationID int64           `json:"exploration_id"`
			Kind          string          `json:"kind"`
			State         string          `json:"state"`
			Payload       json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil, fmt.Errorf("parse final verification node: %w", err)
		}
		if node.Kind == "goal" && node.ExplorationID == expID {
			out.Goals = append(out.Goals, raw)
			goalIDs[node.ID] = true
		}
		if node.Kind != "fact" && node.Kind != "finding" && !(node.Kind == "intent" && node.State == "done") {
			continue
		}
		out.EvidenceTotal++
		// Keep the optional sample small even when a task graph is very large.
		if len(summaries) >= 12 {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(node.Payload, &payload); err != nil {
			return nil, fmt.Errorf("parse final verification evidence summary: %w", err)
		}
		summary := ""
		for _, key := range []string{"summary", "text", "name"} {
			if json.Unmarshal(payload[key], &summary) == nil && summary != "" {
				break
			}
		}
		if runes := []rune(summary); len(runes) > 256 {
			summary = string(runes[:256]) + "…"
		}
		summaries = append(summaries, completionReviewSummary{ID: node.ID, Kind: node.Kind, State: node.State, Summary: summary})
	}
	for _, edge := range input.Data.Edges {
		if edge.Rel == "proves" && goalIDs[edge.Target] {
			out.EvidenceReferences = append(out.EvidenceReferences, completionReviewReference{NodeID: edge.Source, GoalID: edge.Target})
		}
	}
	var work []struct {
		IntentID    int64 `json:"intent_id"`
		ReferenceID int64 `json:"reference_id"`
	}
	if err := json.Unmarshal(out.RemainingWork, &work); err != nil {
		return nil, fmt.Errorf("parse final verification remaining checks: %w", err)
	}
	for _, item := range work {
		if item.ReferenceID != 0 {
			out.EvidenceReferences = append(out.EvidenceReferences, completionReviewReference{NodeID: item.ReferenceID, IntentID: item.IntentID})
		}
	}
	out.EvidenceOmitted = out.EvidenceTotal
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(encoded) > limit {
		return nil, fmt.Errorf("the key final verification input needs %d bytes, exceeding the %d-byte budget; goals, constraints and remaining checks were not truncated; adjust the context window and retry", len(encoded), limit)
	}
	for _, summary := range summaries {
		out.EvidenceSummaries = append(out.EvidenceSummaries, summary)
		out.EvidenceOmitted = out.EvidenceTotal - len(out.EvidenceSummaries)
		candidate, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		if len(candidate) > limit {
			break
		}
		encoded = candidate
	}
	return encoded, nil
}
