//go:build ignore

package server

import (
	"testing"
	"time"
)

// A pause must not spend the task's timeout budget: resuming moves deadline_at by
// the pause length, and both pause paths record when the pause began.
func TestResumeMovesDeadlineByPauseLength(t *testing.T) {
	m, task, stored := p1AdmissionProgressFixture(t, false, false)
	var deadline time.Time
	if err := m.pg.QueryRowContext(t.Context(), `UPDATE tasks SET timeout_seconds=3600, first_run_at=now(), deadline_at=now()+interval '1 hour' WHERE id=$1 RETURNING deadline_at`, stored.ID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyTaskPause(task.ID); err != nil {
		t.Fatal(err)
	}
	var pausedAt *time.Time
	if err := m.pg.QueryRowContext(t.Context(), `SELECT paused_at FROM tasks WHERE id=$1`, stored.ID).Scan(&pausedAt); err != nil || pausedAt == nil {
		t.Fatalf("user pause did not record paused_at: %v %v", pausedAt, err)
	}
	// Simulate a 30-minute pause.
	if _, err := m.pg.ExecContext(t.Context(), `UPDATE tasks SET paused_at=paused_at-interval '30 minutes' WHERE id=$1`, stored.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyTaskAdmission(task.ID, "running", "running", false, "", false); err != nil {
		t.Fatal(err)
	}
	var moved time.Time
	if err := m.pg.QueryRowContext(t.Context(), `SELECT deadline_at, paused_at FROM tasks WHERE id=$1`, stored.ID).Scan(&moved, &pausedAt); err != nil {
		t.Fatal(err)
	}
	if shift := moved.Sub(deadline); shift < 30*time.Minute || shift > 31*time.Minute {
		t.Fatalf("deadline moved by %v, want ~30m", shift)
	}
	if pausedAt != nil {
		t.Fatal("resume must clear paused_at")
	}
	if live := task.lifecycleSnapshot().DeadlineAt; live != moved.Unix() {
		t.Fatalf("live deadline %d, want %d", live, moved.Unix())
	}
	e := NewEngine(m)
	e.deadline.Store(task.ID, deadline.Unix()) // stale value stamped before the pause
	if got := e.taskDeadline(task); got != moved.Unix() {
		t.Fatalf("engine deadline %d, want the moved %d", got, moved.Unix())
	}

	// A review pause (pauseForReview → SetTaskPaused) records paused_at as well.
	if err := m.SetTaskPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.QueryRowContext(t.Context(), `SELECT paused_at FROM tasks WHERE id=$1`, stored.ID).Scan(&pausedAt); err != nil || pausedAt == nil {
		t.Fatalf("review pause did not record paused_at: %v %v", pausedAt, err)
	}
}

// Without a pause, resuming leaves the deadline unchanged.
func TestAdmissionWithoutPauseKeepsDeadline(t *testing.T) {
	m, task, stored := p1AdmissionProgressFixture(t, false, true)
	var deadline time.Time
	if err := m.pg.QueryRowContext(t.Context(), `UPDATE tasks SET timeout_seconds=3600, first_run_at=now(), deadline_at=now()+interval '1 hour' WHERE id=$1 RETURNING deadline_at`, stored.ID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyTaskAdmission(task.ID, "running", "running", false, "", false); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := m.pg.QueryRowContext(t.Context(), `SELECT deadline_at FROM tasks WHERE id=$1`, stored.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(deadline) {
		t.Fatalf("deadline changed from %v to %v without a pause", deadline, after)
	}
}
