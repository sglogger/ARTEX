package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/harness"
)

// runTrace retains the latest tool call so an interrupted run can identify the
// operation that was still in flight.
type runTrace struct {
	startedAt time.Time
	id        string
	name      string
	input     string
	at        time.Time
	pending   bool
}

func (t *runTrace) start(id, name, input string) {
	t.id, t.name, t.input, t.at, t.pending = id, name, input, time.Now(), true
}

func (t *runTrace) done(id string) {
	if id == t.id {
		t.pending = false
	}
}

var reasonHint = map[harness.TerminalReason]string{
	harness.ReasonSettlementRequested: "A task wrap-up request was received; this round only organizes and saves existing evidence; unfinished checks must still pass final verification and cannot be treated as successfully completed",
	harness.ReasonCompleted:           "The model ended this round normally but left no text summary; facts and assets are as recorded by this round's tool calls",
	harness.ReasonMaxTurns:            "Reached the step limit (MaxTurns): the SDK ran the wrap-up and wrote back facts and assets; the intent is marked exhausted so the planner can continue in another direction, rather than being treated as a failure",
	harness.ReasonTimeout:             "Reached the wall-clock budget of a single run (MaxDuration): when time is up running tools are interrupted and wrap-up happens in place, writing back the identified facts and assets; the intent is marked exhausted",
	harness.ReasonModelError:          "The model or API call failed (network, authentication, rate limiting, provider 5xx, etc.); after retries ran out the intent is marked blocked — a transport-layer failure meant this intent was barely explored; check its execution (get_worker_trace) before deciding to re-dispatch or change approach",
	harness.ReasonBlockingLimit:       "The context length reached the hard limit and the request was intercepted before being sent; narrow the intent granularity or compress tool results",
	harness.ReasonPromptTooLong:       "The prompt is too long and the context compaction retries are exhausted; execution cannot continue",
	harness.ReasonImageError:          "The current model doesn't support this round's multimodal content; switch to a vision-capable model or avoid tools returning images",
	harness.ReasonStopHookPrevented:   "A Stop hook prevented this round from ending, and it then failed to continue; check whether the task's Guard rules are too strict",
	harness.ReasonHookStopped:         "A tool or hook deliberately stopped further execution, e.g. an out-of-scope target or a forbidden command; check the intercept explanation in the last tool_result",
	harness.ReasonAbortedStreaming:    "The run was cancelled while the model output was streaming",
	harness.ReasonAbortedTools:        "The run was cancelled while tools were executing",
}

// terminalText renders a terminal event with no final text into a compact summary
// and a Markdown detail block.
func terminalText(ctx context.Context, term *harness.Terminal, tr *runTrace) (string, string) {
	reason := term.Reason
	aborted := reason == harness.ReasonAbortedStreaming || reason == harness.ReasonAbortedTools
	// Prompt may return ctx.Err directly without a terminal event. Preserve the
	// cancellation cause instead of falling back to an empty/unknown terminal reason.
	if reason == "" && ctx.Err() != nil {
		aborted = true
	}

	var sum string
	if aborted {
		_, short, _, ok := AbortReason(ctx)
		if !ok {
			short = "could not obtain the cancellation cause"
		}
		stage := "during execution"
		switch reason {
		case harness.ReasonAbortedStreaming:
			stage = "the model output stage"
		case harness.ReasonAbortedTools:
			stage = "the tool execution stage"
		}
		sum = "(Run interrupted: " + short + "; stopped at " + stage + progressSuffix(term, tr) + ", not completed)"
	} else if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		sum = "(Reached the run budget limit (" + string(reason) + "); wrap-up wrote back facts" + progressSuffix(term, tr) + "; no text summary this time)"
	} else {
		hint := terminalReasonHint(reason)
		sum = "(No text summary, terminal state " + terminalReasonLabel(reason) + ": " + firstLine(hint, 80) + ")"
	}

	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	displayReason := terminalReasonLabel(reason)
	fmt.Fprintf(&b, "- **Terminal state**: `%s` - %s\n", displayReason, terminalReasonHint(reason))
	if aborted {
		code, _, why, ok := AbortReason(ctx)
		if ok {
			fmt.Fprintf(&b, "- **Interruption cause** (`%s`): %s\n", code, why)
		} else {
			b.WriteString("- **Interruption cause**: unavailable; the canceller may not have attached a named cause via context.WithCancelCause\n")
		}
	}
	if term.Err != nil {
		fmt.Fprintf(&b, "- **Underlying error**: `%v`\n", term.Err)
	}
	if aborted && strings.TrimSpace(term.Text) != "" {
		b.WriteString("- **Partial output generated before cancellation**:\n\n")
		b.WriteString(term.Text)
		b.WriteString("\n\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **Executed**: %d model turns\n", term.Turns)
	}
	if !tr.startedAt.IsZero() {
		fmt.Fprintf(&b, "- **Duration of this run**: %s\n", roundDur(time.Since(tr.startedAt)))
	}
	if u := term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "- **Cumulative tokens**: input %d / output %d / cache read %d / cache write %d\n",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name == "" {
		b.WriteString("- **Tool calls**: this run ended before issuing any tool call\n")
	} else if tr.pending {
		fmt.Fprintf(&b, "- **Tool executing at interruption**: `%s` (running for %s, **no result returned**)\n\n  ```json\n  %s\n  ```\n",
			tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
	} else {
		fmt.Fprintf(&b, "- **Last tool before interruption**: `%s` (returned normally)\n", tr.name)
	}
	return sum, b.String()
}

func terminalReasonLabel(reason harness.TerminalReason) string {
	if reason == "" {
		return "context_canceled"
	}
	return string(reason)
}

func terminalReasonHint(reason harness.TerminalReason) string {
	if hint := reasonHint[reason]; hint != "" {
		return hint
	}
	if reason == "" {
		return "The run's context was cancelled, but no Terminal event was produced underneath"
	}
	return "Unknown terminal state; harness may have added a new TerminalReason — add it to reasonHint"
}

func progressSuffix(term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d turns", term.Turns))
	}
	if !tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return ", ran " + strings.Join(parts, " / ")
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Minute).String()
	}
}
