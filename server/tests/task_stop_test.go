//go:build ignore

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
)

// newStopTestServer is newAdmissionTestServer with a live root context, so the
// stop wrap-up routine actually runs. No agents resolve, which makes the final
// verification report "model not ready" and keeps the test free of LLM calls.
func newStopTestServer(t *testing.T, m *Manager) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e := NewEngine(m)
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return nil, nil })
	return &Server{m: m, engine: e, ctx: ctx, chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{}}
}

func TestRequestTaskStopPersistsOnceAndRejectsTerminalTasks(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	task, err := m.CreateTask("stop request", "record a user stop", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()

	fresh, err := m.RequestTaskStop(task.ID)
	if err != nil || !fresh {
		t.Fatalf("first stop request: fresh=%v err=%v", fresh, err)
	}
	first := task.lifecycleSnapshot().StopRequestedAt
	if first == 0 {
		t.Fatal("stop request was not mirrored on the live task")
	}
	stored, err := m.pg.GetTask(mustTaskID(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.StopRequestedAt == nil || stored.Queued {
		t.Fatalf("stop request was not persisted or left the task queued: %+v", stored)
	}

	fresh, err = m.RequestTaskStop(task.ID)
	if err != nil || fresh {
		t.Fatalf("repeated stop request must be a no-op: fresh=%v err=%v", fresh, err)
	}
	if got := task.lifecycleSnapshot().StopRequestedAt; got != first {
		t.Fatalf("repeated stop request moved the timestamp: %d -> %d", first, got)
	}

	if err := m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RequestTaskStop(task.ID); err == nil {
		t.Fatal("a finished task accepted a stop request")
	}
}

func TestStopControlWrapsUpToStoppedAndRevivalClearsIt(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	task, err := m.CreateTask("stop wrap-up", "end as stopped", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()

	s := newStopTestServer(t, m)
	result, err := s.applyTaskControl(task, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "stopping" {
		t.Fatalf("stop control status = %q, want stopping", result.Status)
	}
	if _, err := s.applyTaskControl(task, "stop"); err != nil {
		t.Fatalf("a second stop while wrapping up must be accepted idempotently: %v", err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for task.lifecycleSnapshot().Status != "stopped" {
		if time.Now().After(deadline) {
			t.Fatalf("task did not reach stopped; lifecycle=%+v", task.lifecycleSnapshot())
		}
		time.Sleep(100 * time.Millisecond)
	}
	stored, err := m.pg.GetTask(mustTaskID(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.Status != "stopped" || stored.CompletedAt == nil {
		t.Fatalf("stopped status was not committed: %+v", stored)
	}
	if got := s.resolvedTaskStatus(task); got != "stopped" {
		t.Fatalf("resolved status = %q, want stopped", got)
	}
	if _, err := s.applyTaskControl(task, "stop"); err == nil || !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("stopping a stopped task must be rejected, got %v", err)
	}
	if _, err := s.applyTaskControl(task, "pause"); err == nil {
		t.Fatal("a stopped task must not be pausable")
	}

	// Reviving (e.g. a new goal or an intent rerun) clears the stop so workers
	// are not held back by the old wrap-up gate.
	if _, err := s.admitTask(task, "resume"); err != nil {
		t.Fatal(err)
	}
	revived := task.lifecycleSnapshot()
	if isTerminalStatus(revived.Status) || revived.StopRequestedAt != 0 {
		t.Fatalf("revived task kept its stop state: %+v", revived)
	}
	if s.engine.isSettling(task.ID) {
		t.Fatal("revived task kept the settling gate")
	}
	if _, ok := s.engine.stopStarted.Load(task.ID); ok {
		t.Fatal("revived task kept the stop dedup flag")
	}
	stored, err = m.pg.GetTask(mustTaskID(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.StopRequestedAt != nil || stored.Status != "running" {
		t.Fatalf("revival did not clear the persisted stop: %+v", stored)
	}
}
