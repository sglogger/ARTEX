package server

import (
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// Server-side resolution of the retry policy, see the LLM retry design doc. Of the five layers:
//   - connect / empty response / same-provider safe window "follow the endpoint"; each LLM profile can override
//     the global default (an empty profile field inherits the global value, and the built-in default applies if the global is unset too);
//   - circuit breaker / intent rerun are process-level, with a single global copy.
//
// The global policy reads one settings row from the DB per call; all call sites are low-frequency paths (building providers, work wrap-up,
// saving config), so another cache layer isn't worth it. Circuit breaker parameters are the exception: they're read on every failure, so
// applyRetryPolicy pushes them to the Registry to keep.

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// Counts keep the raw "0 = default / negative = off" semantics here: the SDK's MaxRetries /
		// EmptyResponseRetries have exactly the same shape, so let it parse them itself.
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// Circuit breaker (rotation cooldown) defaults, identical to llmpool's built-ins; only overridden here when "the user configured a value".
// Intent rerun defaults are modelErrorRetries / modelErrorRetryBackoff in engine.go.

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (see steerHooks.Stop). It deliberately reuses layer ②'s knob, the "empty
// response retry count": both are means to the same end. The SDK layer handles "not a single content block", by
// resending the same request unchanged; this one handles "thinking only, no text and no tools", by appending an instruction so
// the model carries on with its existing thinking (resending unchanged makes no sense for idle turns shaped by the context). The emptiness criteria
// differ because the SDK goes by "whether any event was yielded", and thinking deltas are events themselves; but when users configure
// "how many empty response retries" they mean "if the model produced nothing substantial, try again", so sharing one count across both layers
// matches that mental model.
//
// Reads the global policy rather than a profile override: a run may switch profiles midway through failover, and this
// is a total budget gate for the whole intent that shouldn't change with the endpoint. Semantics match the SDK's emptyRetries():
// 0 = default defaultEmptyTurnNudges; -1 (negative) = disable idle-turn continuation; >0 = use that value.
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
