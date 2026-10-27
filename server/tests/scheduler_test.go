//go:build ignore

package server

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestSchedulerToolCallsQueueOncePerCompletedCall(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no test database configured: %v", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	task, err := pg.CreateTask("multiple asset workers", "write each finding report", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteTask(task.ID) })
	var last int64
	if err := pg.QueryRow(`SELECT COALESCE(max(id),0) FROM activity`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	previousState, err := pg.GetSchedState(schedKeyLastToolCall)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.SetSchedState(schedKeyLastToolCall, previousState) })
	if err := pg.SetSchedState(schedKeyLastToolCall, strconv.FormatInt(last, 10)); err != nil {
		t.Fatal(err)
	}
	store := pg.Exploration(task.ExplorationID)
	rows := []db.Activity{
		{Worker: "asset-a", Kind: "tool_use", Detail: "input-for-asset-a"},
		{Worker: "asset-b", Kind: "tool_use", Detail: "input-for-asset-b"},
		{Worker: "asset-b", Kind: "tool_result", Detail: "finding-for-asset-b"},
		{Worker: "asset-a", Kind: "tool_result", Detail: "finding-for-asset-a"},
		{Worker: "asset-c", Kind: "tool_use", Detail: "input-that-failed"},
		{Worker: "asset-c", Kind: "tool_result", Detail: "finding-write-failed", IsError: true},
	}
	var lastID int64
	for _, row := range rows {
		row.Tool, row.ToolUseID = "report_finding", "call_1"
		lastID, err = store.AppendActivity(row)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Hold the pump stopped so the real scheduler and enqueue path can be tested
	// deterministically without invoking a model or depending on goroutine timing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Server{
		m: &Manager{pg: pg}, ctx: ctx,
		chatAgent:     agent.NewChatAgent(nil, "", t.TempDir(), nil, 0),
		triggerQ:      map[string][]triggeredRun{},
		triggerActive: map[string]int{},
		triggerCfg:    map[string]triggerBehavior{},
	}
	scheduler := &Scheduler{s: s, pg: pg}
	triggers := []*db.AgentTrigger{{OnToolCall: true, ToolNames: []string{"report_finding"}, AgentKey: "reporter"}}
	scheduler.fireToolCalls(triggers)
	queue := s.triggerQ["reporter"]
	if len(queue) != 2 {
		t.Fatalf("two successful calls should enqueue two reports, got %d: %+v", len(queue), queue)
	}
	for i, asset := range []string{"asset-b", "asset-a"} {
		if !strings.Contains(queue[i].message, "input-for-"+asset) || !strings.Contains(queue[i].message, "finding-for-"+asset) {
			t.Errorf("report context mixed between workers: %+v", queue[i])
		}
	}
	if got := scheduler.mustState(schedKeyLastToolCall); got != strconv.FormatInt(lastID, 10) {
		t.Fatalf("watermark=%q, want %d including the failed result", got, lastID)
	}
	scheduler.fireToolCalls(triggers)
	if got := len(s.triggerQ["reporter"]); got != 2 {
		t.Fatalf("next tick replayed queued calls: %d", got)
	}
}
