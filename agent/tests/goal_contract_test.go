//go:build ignore

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestGoalContractSettlerParsesContract(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		ok           bool
		goalType     string
	}{
		{"outcome", `{"goal_type":"outcome","required_checks":[]}`, true, "outcome"},
		{"inspection", `{"goal_type":"inspection","required_checks":["ports 22 and 443 of 10.0.0.5"]}`, true, "inspection"},
		{"inspection_without_checks", `{"goal_type":"inspection","required_checks":[" "]}`, false, ""},
		{"unknown_type", `{"goal_type":"other","required_checks":[]}`, false, ""},
		{"extra_output", `{"goal_type":"outcome","required_checks":[]} trailing`, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := completionReviewProvider{stream: func(_ context.Context, _ llm.CompletionRequest, yield func(llm.StreamEvent, error) bool) {
				completionReviewEvents(yield, llm.StreamEvent{Type: llm.SETextDelta, Text: tc.output})
			}}
			goal := &db.Node{ID: 1, Kind: db.KindGoal, Payload: json.RawMessage(`{"text":"What is the RTT to 9.9.9.9?","goal_contract_pending":true}`)}
			c, err := NewGoalContractSettler(provider)(t.Context(), goal, "What is the RTT to 9.9.9.9?", "")
			if (err == nil) != tc.ok || c.GoalType != tc.goalType {
				t.Fatalf("contract=%+v err=%v", c, err)
			}
		})
	}
}

func TestProofLedgerCachesFinalRejectionsAndCountsStreaks(t *testing.T) {
	l := NewProofLedger()
	snap := db.GoalProofSnapshot{Goal: &db.Node{ID: 7, ContentVersion: 1}, Evidence: &db.Node{ID: 9, ContentVersion: 2}}
	l.Fail(snap, "review timeout", false)
	if l.Rejected(snap) != "" {
		t.Fatal("an infrastructure failure must not be cached as a verdict")
	}
	l.Fail(snap, "no RTT value in the fact", true)
	if got := l.Rejected(snap); got != "no RTT value in the fact" {
		t.Fatalf("cached rejection = %q", got)
	}
	changed := snap
	changed.Evidence = &db.Node{ID: 9, ContentVersion: 3}
	if l.Rejected(changed) != "" {
		t.Fatal("new evidence version must be reviewed again")
	}
	if _, _, _, ok := l.Exhausted(5); ok {
		t.Fatal("two failures must not exhaust a limit of five")
	}
	for range 3 {
		l.Fail(snap, "still rejected", true)
	}
	id, fails, reason, ok := l.Exhausted(5)
	if !ok || id != 7 || fails != 5 || reason != "still rejected" {
		t.Fatalf("exhausted = %d %d %q %v", id, fails, reason, ok)
	}
	if _, _, _, ok := l.Exhausted(5); ok {
		t.Fatal("an exhausted streak must reset so a resumed run gets a fresh allowance")
	}
	l.Fail(snap, "x", true)
	l.Succeed(7)
	if l.Rejected(snap) != "" {
		t.Fatal("success must clear the goal's history")
	}
}

// Reproduces the endless proof loop: decomposition failed, the fallback goal had no
// contract, and every prove_goal was rejected after a full review.
func TestProveGoalSettlesPendingContractBeforeReview(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTask("rtt", "What is the RTT to 9.9.9.9?", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	es := d.Exploration(task.ExplorationID)
	g, err := es.AddGoal(map[string]any{"text": "What is the RTT to 9.9.9.9?", "goal_contract_pending": true}, "system")
	if err != nil {
		t.Fatal(err)
	}
	e, err := es.AddNode(db.KindFact, map[string]any{"summary": "ping 9.9.9.9", "evidence": "rtt min/avg/max = 4.3/5.9/9.4 ms"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(es, "planner")
	ts.SetTaskID(task.ID)
	reviews := 0
	yes := true
	ts.SetGoalProofReviewer(func(context.Context, db.GoalProofSnapshot, string) (GoalProofVerdict, error) {
		reviews++
		return GoalProofVerdict{Supported: &yes, GoalType: "outcome", Reason: "the fact carries the measured RTT"}, nil
	})
	ledger := NewProofLedger()
	ctx := WithProofLedger(t.Context(), ledger)
	input, _ := json.Marshal(map[string]any{"goal_id": g, "evidence_id": e, "reason": "fact has the RTT"})

	ts.SetGoalContractSettler(func(context.Context, *db.Node, string, string) (GoalContract, error) {
		return GoalContract{}, errors.New("context deadline exceeded")
	})
	res, err := ts.proveGoal().Call(ctx, input, nil)
	if err != nil || !res.IsError || !strings.Contains(res.Flatten(), "settling them failed") || reviews != 0 {
		t.Fatalf("unsettled contract: %s err=%v reviews=%d", res.Flatten(), err, reviews)
	}
	n, _ := es.GetNode(g)
	if !db.GoalContractPending(n) || n.State != "open" {
		t.Fatal("a failed settlement changed the goal")
	}

	ts.SetGoalContractSettler(func(context.Context, *db.Node, string, string) (GoalContract, error) {
		return GoalContract{GoalType: "outcome"}, nil
	})
	res, err = ts.proveGoal().Call(ctx, input, nil)
	if err != nil || res.IsError || reviews != 1 {
		t.Fatalf("settled goal not proven: %s err=%v reviews=%d", res.Flatten(), err, reviews)
	}
	n, _ = es.GetNode(g)
	var p struct {
		Type    string `json:"goal_type"`
		Pending bool   `json:"goal_contract_pending"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	if n.State != "met" || p.Type != "outcome" || p.Pending {
		t.Fatalf("goal = %s %s", n.State, n.Payload)
	}
	if ok, err := es.SettleGoalContract(t.Context(), g, "inspection", []string{"x"}); ok || err != nil {
		t.Fatalf("a settled contract was rewritten: %v %v", ok, err)
	}
}

func TestProveGoalDoesNotReReviewIdenticalRejectedEvidence(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTask("rtt", "What is the RTT to 9.9.9.9?", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	es := d.Exploration(task.ExplorationID)
	g, err := es.AddGoal(map[string]any{"text": "What is the RTT to 9.9.9.9?", "goal_type": "outcome"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	e, err := es.AddNode(db.KindFact, map[string]any{"summary": "traceroute not installed"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(es, "planner")
	ts.SetTaskID(task.ID)
	reviews := 0
	no := false
	ts.SetGoalProofReviewer(func(context.Context, db.GoalProofSnapshot, string) (GoalProofVerdict, error) {
		reviews++
		return GoalProofVerdict{Supported: &no, GoalType: "outcome", Reason: "no RTT value"}, nil
	})
	ledger := NewProofLedger()
	ctx := WithProofLedger(t.Context(), ledger)
	input, _ := json.Marshal(map[string]any{"goal_id": g, "evidence_id": e, "reason": "try again"})
	for i := range proofAttemptLimitForTest {
		res, err := ts.proveGoal().Call(ctx, input, nil)
		if err != nil || !res.IsError {
			t.Fatalf("attempt %d accepted: %s %v", i, res.Flatten(), err)
		}
		if i > 0 && !strings.Contains(res.Flatten(), "already rejected in exactly this version") {
			t.Fatalf("attempt %d not short-circuited: %s", i, res.Flatten())
		}
	}
	if reviews != 1 {
		t.Fatalf("identical evidence reviewed %d times, want 1", reviews)
	}
	if id, fails, _, ok := ledger.Exhausted(proofAttemptLimitForTest); !ok || id != g || fails != proofAttemptLimitForTest {
		t.Fatalf("streak not exhausted: %d %d %v", id, fails, ok)
	}
}

const proofAttemptLimitForTest = 5
