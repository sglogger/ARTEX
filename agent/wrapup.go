package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// Wrap-up prompt (wrap-up / settlement prompt): when an agent is terminated because [steps ran out (MaxTurns)] or
// [it timed out (run_seconds/MaxDuration)], the SDK's settlement phase injects this prompt
// so the agent first persists what it identified but didn't write back, then outputs a one-sentence summary, avoiding a half-finished run.
//
// Each agent's wrap-up prompt can be overridden in the admin as needed (stored in agents.wrapup_prompt); empty uses the
// built-in default here. Only the [prompt body] is editable; which tools are disabled and how many turns wrap-up itself gets are fixed code policy.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in default wrap-up prompts, indexed by agent key. worker reuses the historically hardcoded settleWrapUpPrompt
// (defined in worker.go); planner/mainagent each have their own; anything else (custom agents) uses the generic fallback.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: built-in defaults for the turn budget of each agent's wrap-up phase [itself] (can be overridden by an admin value >0).
// All get 10 turns, ensuring the wrap-up phase has enough steps to persist. Misses use genericWrapupTurns.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "The steps for this planning round are about to run out — note that only [this round] is ending; the system will wake you again to keep planning as the situation changes, so the task is not terminating and you don't need to close out the whole plan here. Land the conclusions you've already worked out this round so it isn't wasted, but [don't force intents just to wrap up] (0 intents this round is still a perfectly normal outcome): (1) if you've identified exploration directions that [should be dispatched now], submit them in bulk with one add_intent (don't hold back ones you've thought through); (2) for goals already proven met by some finding/fact, call prove_goal to mark them met (don't miss any); (3) if you've identified a serial exploit chain that needs step-by-step work, record it with TodoWrite so the next wake-up can continue dispatching. When done, simply end this round; no summary text is needed."

const mainAgentWrapUpDefault = "Your steps are about to run out and this interaction is ending. Don't start any new exploration/operation. **In a single sentence of plain text**, summarize the current progress, key conclusions and the suggested next step for the user."

const genericWrapUpDefault = "You are about to be terminated because your budget is exhausted. First write back completed results that haven't been persisted, then **in a single sentence of plain text** summarize what you did and the key conclusions you reached (this sentence is shown as the result of this run)."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- Task-level timeout wrap-up prompts (see the task-level timeout and wrap-up design doc) ----------
//
// These are a [separate set] from the per-run wrap-up prompts: per-run means "the budget for this one run of yours is used up"; task timeout means
// "the whole task is due and about to end". The semantics are often opposite (especially for the planner: per-run says "don't stop, keep planning",
// task timeout says "time's up, stop planning and make the final judgement"). Only configured for worker/planner.

// WrapupTaskTimeoutOverride / …TurnsOverride: DB overrides for the task-timeout wrap-up prompt and turns
// (wired to agents.task_timeout_wrapup_prompt / _max_turns, worker/planner only).
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The whole task has reached its timeout limit and is about to end** (not this run's budget — the entire exploration is out of time). This is your last chance: (1) persist [everything] you've identified but not yet written back — new assets with insert_assets, exploration conclusions/facts with record_fact, confirmed findings with report_finding; (2) don't start any new commands/probes; (3) **finally, in a single sentence of plain text**, summarize your key conclusions on this intent."

const plannerTaskTimeoutDefault = "**The whole task has reached its timeout limit and is about to end** (not this round — the entire task is terminating). Based on [all] current facts and findings, make one last goal judgement: call prove_goal to mark met the goals proven met by evidence (don't miss any). **Don't generate any new intents** (intents dispatched now won't be executed anyway). Once judged, close out; no summary text is needed."

// TaskTimeoutWrapupDefault returns an agent's built-in default task-timeout wrap-up prompt (for admin placeholders/restoring defaults).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // not configured (mainagent/chat) returns an empty string
}

// resolveTaskTimeoutWrapup: DB override (non-empty) > built-in default. An empty string means the agent has no task-timeout prompt
// (not worker/planner); callers should then fall back to the per-run prompt.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // defaults to the per-run turn count
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  → this run is clamped by the task deadline: wrap-up due to Timeout = task is due → task-timeout prompt;
//     wrap-up due to MaxTurns = steps ran out first within the clamped window while the task has minutes left → falls back to the per-run prompt.
//   - clamped=false → the task has time left: both reasons use the per-run prompt (i.e. it degenerates to wrapupSettlement).
//
// The harness's PromptByReason picks at wrap-up time by the [actual] reason, with no build-time mismatch.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // fallback (also the value for both reasons when not clamped)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // task is due
				harness.ReasonMaxTurns: perRun, // steps ran out first, the task still has time
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
