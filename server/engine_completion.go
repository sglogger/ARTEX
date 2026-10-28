package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

type completionRun struct {
	attempt   *db.TaskCompletion
	cancel    context.CancelFunc
	done      chan struct{}
	timedOut  bool
	root      context.Context
	stalled   atomic.Bool
	committed atomic.Bool
}

func (e *Engine) completionStalled(taskID string) bool {
	value, ok := e.completing.Load(taskID)
	return ok && value.(*completionRun).stalled.Load()
}

func (e *Engine) isCompleting(taskID string) bool {
	_, ok := e.completing.Load(taskID)
	return ok
}

// Execution admission and completion installation share one mutex. Deletion's
// broader writer barrier remains independent so reads and human edits continue.
func (e *Engine) beginExecutionOperation(taskID string) bool {
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	e.completionMu.Lock()
	defer e.completionMu.Unlock()
	if e.IsDeleting(taskID) || e.IsPaused(taskID) || e.isSettling(taskID) || e.isCompleting(taskID) {
		return false
	}
	if e.m != nil && isTerminalStatus(e.m.TaskStatus(taskID)) {
		return false
	}
	atomic.AddInt64(e.inflightCounter(taskID), 1)
	return true
}

// requestCompletion installs the barrier before requesting checkpoint-only
// execution. It never joins inline: its caller may itself own an inflight slot.
func (e *Engine) requestCompletion(ctx context.Context, t *Task, reason string, decisions []db.CompletionDisposition, timedOut bool) error {
	profile := ""
	if planner, _ := e.snapshotFor(t); planner != nil {
		profile = planner.CompletionReviewSignature()
	}
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	e.completionMu.Lock()
	defer e.completionMu.Unlock()
	if e.IsDeleting(t.ID) || e.isCompleting(t.ID) || (!timedOut && (e.IsPaused(t.ID) || e.isSettling(t.ID))) {
		return db.ErrCompletionChanged
	}
	id, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil {
		return err
	}
	requestCtx, cancelRequest := context.WithTimeout(ctx, 10*time.Second)
	defer cancelRequest()
	attempt, err := e.m.pg.BeginTaskCompletion(requestCtx, id, reason, decisions, time.Now().Add(settleDrainGrace), timedOut, profile)
	if err != nil {
		return err
	}
	rt := e.registerTaskRoutines(ctx, t.ID, 1)
	completionCtx, cancel := context.WithCancel(rt.ctx)
	run := &completionRun{attempt: attempt, cancel: cancel, done: make(chan struct{}), timedOut: timedOut, root: rt.ctx}
	// This lease covers the coordinator and any late tool callback. Deletion and
	// resource accounting cannot observe zero while checkpointing is unresolved.
	atomic.AddInt64(e.inflightCounter(t.ID), 1)
	e.completing.Store(t.ID, run)
	e.execMu.Lock()
	for _, control := range e.execControllers[t.ID] {
		control.Request()
	}
	e.execMu.Unlock()
	runTaskRoutine(rt, func(context.Context) { e.completeTask(completionCtx, t, run) })
	return nil
}

func (e *Engine) completeTask(ctx context.Context, t *Task, run *completionRun) {
	committed, forced, joined := false, false, false
	reason := "completion was cancelled; evidence kept for another review"
	failureKind := ""
	var goalGaps []db.CompletionGoalGap
	defer func() {
		defer e.decInflight(t.ID)
		run.cancel()
		// The runtime gate stays installed until recovery has persisted. If that
		// write fails, retries keep execution paused at the gate rather than lose
		// the durable decision or revive workers against an active attempt.
		if !committed {
			failureSaved := failureKind == "" || run.timedOut
			for {
				recoverCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
				var err error
				if !failureSaved {
					err = e.m.pg.RecordTaskCompletionFailure(recoverCtx, run.attempt.TaskID, run.attempt.AttemptID, failureKind, reason, goalGaps...)
					failureSaved = err == nil
				}
				if err == nil {
					err = e.m.pg.InterruptTaskCompletion(recoverCtx, run.attempt.TaskID, run.attempt.AttemptID, reason, true)
				}
				cancel()
				if err == nil {
					break
				}
				log.Printf("[completion] task %s failed to save interrupted state: %v", t.ID, err)
				if run.root.Err() != nil || sleepCtx(run.root, time.Second) {
					break
				}
			}
		}
		e.completionMu.Lock()
		// All normal executions have exited before reusing their task context.
		// A pause/deletion may cancel early; its context remains cancelled and is
		// not reset here until those admitted executions really leave.
		if joined {
			e.execMu.Lock()
			if cancel := e.execCancel[t.ID]; cancel != nil {
				cancel(agent.AbortGoalMet)
			}
			delete(e.execCtx, t.ID)
			delete(e.execCancel, t.ID)
			delete(e.execSettlement, t.ID)
			delete(e.execControllers, t.ID)
			e.execMu.Unlock()
		}
		e.completing.CompareAndDelete(t.ID, run)
		e.completionMu.Unlock()
		close(run.done)
		if !committed && !run.timedOut && (failureKind == "" || failureKind == "evidence" || failureKind == "snapshot") {
			e.completionReplan.Store(t.ID, reason)
		}
		if !isTerminalStatus(e.m.TaskStatus(t.ID)) && !e.IsPaused(t.ID) && !e.IsDeleting(t.ID) {
			t.Notify()
		}
	}()

	e.emitActivity(t, db.Activity{Worker: "system", Kind: "text", Summary: "Completing: no longer claiming new intents, waiting for current work to save results and exit"})
	// Cancellation requests do not prove quiescence. Even after a forced stop,
	// wait for actual completion of Execute and its final state/output writes.
	for e.inflightCount(t.ID) > 1 {
		if !forced && time.Now().After(run.attempt.DeadlineAt) {
			forced = true
			e.cancelExec(t.ID, agent.AbortSettleDrainTimeout)
		}
		if forced && time.Now().After(run.attempt.DeadlineAt.Add(10*time.Second)) {
			e.quarantineCompletion(t, run)
		}
		if sleepCtx(run.root, 100*time.Millisecond) {
			return
		}
	}
	e.execMu.Lock()
	controls := append([]*harness.SettlementController(nil), e.execControllers[t.ID]...)
	e.execMu.Unlock()
	for _, control := range controls {
		// The SDK may have returned cancellation before the underlying callback
		// exits. That callback still owns this lease, so join it explicitly.
		for {
			waitCtx, cancel := context.WithTimeout(run.root, time.Second)
			err := control.WaitForTools(waitCtx)
			cancel()
			if err == nil {
				break
			}
			if run.root.Err() != nil {
				return
			}
			if time.Now().After(run.attempt.DeadlineAt) && !forced {
				forced = true
				e.cancelExec(t.ID, agent.AbortSettleDrainTimeout)
			}
			if time.Now().After(run.attempt.DeadlineAt.Add(10 * time.Second)) {
				e.quarantineCompletion(t, run)
			}
		}
	}
	joined = true
	if run.stalled.Load() {
		reason = "the execution that had not exited has now finished; the task stays paused, check the saved results before continuing"
		failureKind = "drain"
		return
	}
	if forced && !run.timedOut {
		reason = "work did not exit normally within the wrap-up deadline; results kept and replanning"
		failureKind = "drain"
		return
	}
	if ctx.Err() != nil {
		return
	}
	if !run.timedOut && (e.IsPaused(t.ID) || e.isSettling(t.ID)) {
		return
	}
	planner, _ := e.snapshotFor(t)
	options := (agent.ReviewOptions{}).Normalized()
	profile := ""
	if planner != nil {
		options = planner.CompletionReviewOptions()
		profile = planner.CompletionReviewSignature()
	}
	reviewDeadline := time.Now().Add(options.Duration)
	if !run.timedOut {
		if absolute := e.taskDeadline(t); absolute > 0 && time.Unix(absolute, 0).Before(reviewDeadline) {
			reviewDeadline = time.Unix(absolute, 0)
		}
	}
	reviewCtx, cancelReview := context.WithDeadline(ctx, reviewDeadline)
	defer cancelReview()
	current, err := e.m.pg.ReviewTaskCompletion(reviewCtx, run.attempt.TaskID, run.attempt.AttemptID, db.CompletionReviewPolicy{Budget: options.Duration, Profile: profile})
	if err != nil {
		reason = fmt.Sprintf("completion snapshot changed, replanning required: %v", err)
		failureKind = "snapshot"
		return
	}
	var input db.CompletionInput
	if err := json.Unmarshal(current.Snapshot, &input); err != nil {
		reason = err.Error()
		return
	}
	met := false
	if planner == nil {
		reason = "final verification model is not ready; cannot confirm completion"
		failureKind = "configuration"
	} else if run.timedOut && e.IsPaused(t.ID) {
		reason = "the task reached its absolute deadline while paused; no final completion verification was run"
	} else {
		e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round", Summary: fmt.Sprintf("Read-only final completion verification (round %d)", e.nextPlannerRound(t.ID))})
		e.BeginLLMCall(t.ID)
		var verdict db.CompletionReviewVerdict
		verdict, err = planner.ReviewCompletionVerdict(reviewCtx, run.attempt.TaskID, t.Store, current.Snapshot, func(a db.Activity) { e.emitActivity(t, a) })
		met, reason, goalGaps = verdict.Complete, verdict.Reason, verdict.GoalGaps
		e.EndLLMCall(t.ID)
		if err != nil {
			met = false
			reason = fmt.Sprintf("final completion verification failed: %v", err)
			failureKind = completionFailureKind(err)
		} else if !met {
			failureKind = "evidence"
			if reason == "final verification submitted no clear verdict" {
				failureKind = "no_verdict"
			}
		}
		if planner.CompletionReviewSignature() != profile {
			met = false
			reason = "model or budget config changed during final verification; another review is required"
			failureKind = "configuration"
		}
		if ctx.Err() != nil {
			failureKind = ""
		}
	}
	status := "done"
	if !met || !input.Eligible || forced {
		if !run.timedOut {
			return
		}
		status = "timeout"
	}
	// A timeout attempt may confirm evidence already complete at the deadline;
	// ordinary attempts must yield to the absolute timeout coordinator.
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	e.completionMu.Lock()
	defer e.completionMu.Unlock()
	if e.IsDeleting(t.ID) || ctx.Err() != nil || (!run.timedOut && (e.IsPaused(t.ID) || e.isSettling(t.ID))) {
		return
	}
	e.m.taskStateMu.Lock()
	defer e.m.taskStateMu.Unlock()
	if err := e.m.pg.FinishTaskCompletion(ctx, current.TaskID, current.AttemptID, current.InputDigest, status, reason); err != nil {
		reason = fmt.Sprintf("data or lifecycle changed before commit; the previous final verification is invalid: %v", err)
		failureKind = "snapshot"
		return
	}
	t.updateLifecycle(func(state *taskLifecycleState) { state.Status = status; state.CompletedAt = time.Now().Unix() })
	committed = true
	run.committed.Store(true)
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "text", Summary: "Completion finished: " + status, Detail: reason})
}

func completionFailureKind(err error) string {
	var history *llm.HistoryError
	var request *llm.RequestError
	var httpErr *llm.HTTPError
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "budget"
	case errors.As(err, &history):
		return "history"
	case errors.As(err, &request):
		return "configuration"
	case errors.As(err, &httpErr) && httpErr.StatusCode >= 400 && httpErr.StatusCode < 500 && httpErr.StatusCode != 408 && httpErr.StatusCode != 429:
		return "configuration"
	default:
		return "transient"
	}
}

func (e *Engine) retryCompletion(ctx context.Context, t *Task) {
	if e.IsPaused(t.ID) || e.IsDeleting(t.ID) || e.isCompleting(t.ID) || e.isSettling(t.ID) || isTerminalStatus(t.lifecycleSnapshot().Status) {
		return
	}
	id, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil {
		return
	}
	if due, err := e.m.pg.TaskCompletionRetryDue(ctx, id); err != nil || !due {
		return
	}
	c, err := e.m.pg.TaskCompletion(ctx, id)
	if err != nil || c == nil || c.Phase != "interrupted" || c.NextRetryAt == nil || time.Now().Before(*c.NextRetryAt) {
		return
	}
	var input db.CompletionInput
	if json.Unmarshal(c.Snapshot, &input) != nil {
		return
	}
	var decisions []db.CompletionDisposition
	for _, w := range input.Work {
		if w.Kind != "" {
			decision := w.CompletionDisposition
			decision.IntentID = w.IntentID
			decisions = append(decisions, decision)
		}
	}
	if err := e.requestCompletion(ctx, t, "re-reviewing per the persisted backoff plan", decisions, false); err != nil {
		var limited *db.CompletionRetryError
		if !errors.As(err, &limited) && !errors.Is(err, db.ErrCompletionChanged) {
			e.completionReplan.Store(t.ID, "inputs changed on retry, remaining work needs checking: "+err.Error())
			t.Notify()
		}
	}
}

func (e *Engine) quarantineCompletion(t *Task, run *completionRun) {
	if !run.stalled.CompareAndSwap(false, true) {
		return
	}
	reason := "completion failed: the previous execution did not exit after cancellation; the task is isolated and paused. Continuing is blocked until it really exits, and existing results may still be saving"
	e.completionMu.Lock()
	e.paused.Store(t.ID, true)
	run.cancel()
	e.completionMu.Unlock()
	e.cancelExec(t.ID, agent.AbortSettleDrainTimeout)
	if err := e.m.ApplyTaskPause(t.ID); err != nil {
		log.Printf("[completion] task %s failed to persist isolation pause: %v", t.ID, err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(run.root), 8*time.Second)
	defer cancel()
	if err := e.m.pg.InterruptTaskCompletion(ctx, run.attempt.TaskID, run.attempt.AttemptID, reason, false); err != nil {
		log.Printf("[completion] task %s failed to persist isolation reason: %v", t.ID, err)
	}
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "text", Summary: reason})
}

// Timeout supersedes a normal attempt, joins its cancellation, then uses the
// same durable checkpoint/review/commit protocol. Retry transient snapshot or
// lock failures until the lifecycle changes or this runtime is shut down.
func (e *Engine) settleTask(ctx context.Context, t *Task) {
	e.completionMu.Lock()
	e.markSettling(t.ID)
	if value, ok := e.completing.Load(t.ID); ok {
		value.(*completionRun).cancel()
	}
	e.completionMu.Unlock()
	for ctx.Err() == nil && !isTerminalStatus(e.m.TaskStatus(t.ID)) && !e.IsDeleting(t.ID) {
		if value, ok := e.completing.Load(t.ID); ok {
			select {
			case <-ctx.Done():
				return
			case <-value.(*completionRun).done:
			}
			if value.(*completionRun).committed.Load() {
				return
			}
			continue
		}
		err := e.requestCompletion(ctx, t, "task reached its absolute deadline", nil, true)
		if err != nil && !errors.Is(err, db.ErrCompletionChanged) {
			log.Printf("[completion] task %s timeout completion retry: %v", t.ID, err)
		}
		if err != nil && sleepCtx(ctx, time.Second) {
			return
		}
	}
}
