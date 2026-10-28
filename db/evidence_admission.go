package db

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateGoalEvidence prevents explicit incomplete/inferred results from being
// promoted even when a model returns a positive verdict. Semantic sufficiency
// still requires independent review of the saved mechanism and observations.
func ValidateGoalEvidence(snapshot GoalProofSnapshot) error {
	if snapshot.Goal == nil || snapshot.Evidence == nil {
		return fmt.Errorf("missing goal or evidence")
	}
	var goal struct {
		Type    string `json:"goal_type"`
		Pending bool   `json:"goal_contract_pending"`
	}
	var evidence struct {
		Confidence string      `json:"confidence"`
		Checkpoint *Checkpoint `json:"checkpoint"`
	}
	if err := json.Unmarshal(snapshot.Goal.Payload, &goal); err != nil {
		return err
	}
	if goal.Pending {
		return fmt.Errorf("goal decomposition has not settled a check or outcome contract yet; specify the goal type and required checks first")
	}
	if err := json.Unmarshal(snapshot.Evidence.Payload, &evidence); err != nil {
		return err
	}
	if evidence.Confidence == "inferred" {
		return fmt.Errorf("an inferred fact alone cannot prove a goal is met")
	}
	if c := evidence.Checkpoint; c != nil {
		if err := c.Validate(); err != nil {
			return err
		}
		if c.Result == "partial" || c.Result == "blocked" {
			return fmt.Errorf("the check still has gaps and cannot prove the goal is complete")
		}
		if goal.Type == "outcome" && c.Result != "positive" {
			return fmt.Errorf("a negative check cannot satisfy an outcome goal")
		}
	}
	var findings []struct {
		Evidence string `json:"evidence"`
		Status   string `json:"status"`
	}
	if len(snapshot.Finding) > 0 {
		if err := json.Unmarshal(snapshot.Finding, &findings); err != nil {
			return err
		}
	}
	for _, f := range findings {
		if strings.TrimSpace(f.Evidence) == "" {
			return fmt.Errorf("the finding lacks verifiable evidence; keep the lead and add evidence, it cannot count as a verified outcome")
		}
		if f.Status == FindingFalsePositive || f.Status == FindingIgnored || f.Status == FindingDuplicate {
			return fmt.Errorf("the finding's current triage status does not support an independent outcome")
		}
	}
	return nil
}
