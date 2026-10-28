package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// compactIntents distills intents to {id, summary, state, asset_ids, parents,
// yields} so the planner sees both the direction and its LINEAGE — parents (the
// upstream nodes it derived from: facts/intents/findings) and yields (the facts/
// findings it produced) — without pulling full payloads. parentsOf/yieldsOf are
// built from the exploration edges in graph_overview.
func compactIntents(ns []*db.Node, parentsOf, yieldsOf map[int64][]int64) []map[string]any {
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		m := map[string]any{"id": n.ID, "summary": p["summary"], "state": n.State}
		if n.Inherited {
			m["source_task_id"] = n.SourceTaskID
			m["inherited"] = true
		}
		// asset_ids is the structured "which assets this direction covers" signal for
		// dedup; fall back to legacy payload keys (target_ids plural, then target_id
		// single) so intents stored before the rename still surface their anchors.
		if tg, ok := p["asset_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_id"]; ok && tg != nil && tg != "" {
			m["asset_ids"] = []any{tg}
		}
		if ps := parentsOf[n.ID]; len(ps) > 0 {
			m["parents"] = ps // upstream: which nodes this intent derives from (several facts can jointly produce one intent)
		}
		if ys := yieldsOf[n.ID]; len(ys) > 0 {
			m["yields"] = ys // downstream: which facts/findings this intent produced
		}
		out = append(out, m)
	}
	return out
}

// ToolSet exposes the PG-backed dual graph (asset + exploration) to an LLM agent.
// One ToolSet is created per planner/worker run; per-run signals live here.
type ToolSet struct {
	findingRecorder FindingRecorder
	goalReviewer    GoalProofReviewer
	as              *db.AssetStore   // asset store (optional; nil = asset tools not available)
	cs              *db.CompanyStore // company store (optional)
	ts              *db.ExplorationStore
	worker          string
	taskID          int64 // PG tasks.id; 0 when unknown (tests / orchestrator cross-task reads)
	// coverageDisabled mirrors tasks.coverage_enabled=false. Stored inverted so the
	// zero value (all existing ToolSet constructions) means ENABLED — matching the
	// DB default (true). When true: graphOverviewData drops the coverage block, the
	// auto-scope hook (insertAssets) is skipped, and add_task_scope/list_untested_assets
	// are filtered out of the agent's tool list. The scope field stays regardless.
	coverageDisabled bool
	// ownerNode is the exploration node that writes attach to: assets this run
	// touches get anchored to it as lineage/provenance (NOT visibility — the asset
	// graph is global and shared). Worker = its claimed intent; planner = begin root.
	ownerNode          int64
	GoalMet            bool
	Reason             string
	completionProposal *CompletionProposal
	completionResultMu sync.Mutex
	writes             WriteCounts
	// killWork, if set, terminates a running work by intent id (engine callback,
	// wired by the planner). nil = the kill_work tool reports unavailable.
	killWork func(intentID int64) error
	// steerWork, if set, queues a mid-run course-correction for the work running an
	// intent id (engine callback, wired by the planner): the worker injects it before
	// its next tool call and re-plans, without being killed. nil = tool unavailable.
	steerWork func(intentID int64, msg string) error
	// enrich, if set, receives async auto-completion triggers (DNS resolve for a
	// domain, HTTP probe for a site). nil = no engine enrichment.
	enrich EnrichTrigger
	// notify, if set, wakes the task's planner after a graph change that should be
	// re-planned promptly (currently: a new hint). nil = no wake (the hint is still
	// stored and read on the next round triggered by other events). debounced.
	notify func()
	// notifyFinding, if set, wakes the task's planner when this run reports a finding,
	// carrying (intentID, summary) so the round can spell out which intent found what.
	// Wired for workers; nil elsewhere → falls back to notify (bare wake).
	notifyFinding func(intentID int64, summary string)
	// resumeTask, if set, revives the task after a graph change that should make a
	// stopped task run again (currently: set_goals adds a goal). It flips a terminal/
	// paused task back to running and (re)starts the engine loops — a plain notify()
	// can't, because the planner's terminal gate swallows wakes. Wired ONLY for the
	// main agent (human steering); nil for the goals decomposer and workers.
	resumeTask func()
	// notifyGoal, if set, wakes the planner AND records ONE "human added N goals: …" trigger
	// for a whole set_goals call (batch-aware — one call, one trigger, not one per goal)
	// so the next round spells out the added goals (instead of the planner having to
	// spot new open goals in the overview). Wired ONLY for the main agent; nil for the
	// goals decomposer (round-0 has no running planner to inform) and workers → those
	// fall back to the bare notify.
	notifyGoal func(texts []string)
	// notifyHint, if set, wakes the planner AND records ONE "human added N strategic hints: …"
	// trigger for a whole add_hint call (batch-aware — one call, one trigger) so the next
	// round is told the round was fired by a new hint and spells the hint out, instead of
	// the planner having to spot it folded into the graph overview. Wired for the main
	// agent + cross-task orchestration; nil elsewhere → falls back to the bare notify.
	notifyHint func(texts []string)
}

// SetNotifyGoal wires the goal-add trigger callback (see ToolSet.notifyGoal). Set only
// by the main-agent chat, so runtime-added goals are announced to the planner by name.
func (t *ToolSet) SetNotifyGoal(fn func([]string)) { t.notifyGoal = fn }

// SetNotifyHint wires the hint-add trigger callback (see ToolSet.notifyHint). Set by
// the main-agent chat and cross-task orchestration, so a runtime-added hint fires a
// planner round announced by name instead of a bare wake.
func (t *ToolSet) SetNotifyHint(fn func([]string)) { t.notifyHint = fn }

// SetResumeTask wires the task-revive callback (see ToolSet.resumeTask). Set only by
// the main-agent chat, so runtime-added goals can pull a finished task back to running.
func (t *ToolSet) SetResumeTask(fn func()) { t.resumeTask = fn }

// SetNotify wires the planner-wake callback (see ToolSet.notify). Set by callers
// that hold the task handle (main-agent chat, cross-task orchestration).
func (t *ToolSet) SetNotify(fn func()) { t.notify = fn }

// SetNotifyFinding wires the finding-wake callback (see ToolSet.notifyFinding).
func (t *ToolSet) SetNotifyFinding(fn func(int64, string)) { t.notifyFinding = fn }

// EnrichTrigger is the enrichment engine seen from the tool layer (see package
// enrich). Kept as an interface here to avoid coupling agent → enrich.
type EnrichTrigger interface {
	ResolveDomain(id int64, host string)
	ProbeSite(id int64, url string)
}

// WriteCounts breaks down what a worker persisted this run, by node kind, so the
// engine can log an accurate "wrote back" summary instead of lumping assets and
// findings under "facts" (record_fact → Facts, insert_assets → Assets,
// report_finding → Findings; each element of a batch counts once).
type WriteCounts struct {
	Facts    int
	Assets   int
	Findings int
}

// Total is every node persisted this run, regardless of kind — the
// "explored but persisted nothing" signal (Total == 0).
func (w WriteCounts) Total() int { return w.Facts + w.Assets + w.Findings }

// String renders the per-kind breakdown for logs, e.g. "facts=1 assets=25 findings=0".
func (w WriteCounts) String() string {
	return fmt.Sprintf("facts=%d assets=%d findings=%d", w.Facts, w.Assets, w.Findings)
}

// Writes reports what this run wrote back, split by node kind (so the engine can
// tell "explored but persisted nothing" apart from a completed intent, and log an
// honest breakdown instead of calling assets/findings "facts").
func (t *ToolSet) Writes() WriteCounts { return t.writes }

func NewToolSet(ts *db.ExplorationStore, worker string) *ToolSet {
	return &ToolSet{ts: ts, worker: worker}
}

// SetTaskID sets the PG task id on this ToolSet so that report_finding can
// dual-write to the standalone findings table (which survives task deletion).
func (t *ToolSet) SetTaskID(id int64) { t.taskID = id }

// SetCoverageEnabled records whether this task has the asset-coverage feature on
// (default enabled). Passing false makes graphOverviewData omit the coverage block
// and DropCoverageTools filter the two coverage-only tools out of the agent's tool
// list. It does NOT stop scope accumulation: insertAssets' auto-scope hook runs
// either way, because task_scope is the task's range boundary (the filter basis for
// asset queries), not merely a coverage denominator.
func (t *ToolSet) SetCoverageEnabled(enabled bool) { t.coverageDisabled = !enabled }

// CoverageDisabled reports whether the coverage feature is off for this task.
func (t *ToolSet) CoverageDisabled() bool { return t.coverageDisabled }

// coverageOnlyTools are the LLM tools that only make sense when asset coverage is
// on. When the feature is off they are filtered out of the agent's tool list so
// they neither pollute the prompt nor let the model build a disabled denominator.
// add_task_scope is deliberately NOT here: task_scope is the task's range boundary
// (the filter basis for asset queries), not merely a coverage denominator, so the
// agents that own scope definition keep it either way — in lockstep with insertAssets'
// auto-scope hook, which also runs regardless of the switch.
var coverageOnlyTools = map[string]bool{"list_untested_assets": true}

// DropCoverageTools returns tools with the coverage-only ones removed when this
// task has the feature disabled; otherwise it returns tools unchanged.
func (t *ToolSet) DropCoverageTools(tools []actool.CoreTool) []actool.CoreTool {
	if !t.coverageDisabled {
		return tools
	}
	out := tools[:0:0]
	for _, tool := range tools {
		if coverageOnlyTools[tool.Name()] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// Cross-task reuse: exported accessors returning the per-task tool logic bound to
// THIS ToolSet's store. Host-side orchestration tools build a ToolSet for an
// arbitrary task, then Call these — so cross-task reads/hint reuse the exact
// same logic as the in-task tools. (readTool ignores ToolContext, so Call(…,nil)
// is safe; add_hint is a writeTool but also doesn't deref the context here.)
func (t *ToolSet) GraphOverviewTool() actool.CoreTool      { return t.graphOverview() }
func (t *ToolSet) ListFindingsTool() actool.CoreTool       { return t.listFindings() }
func (t *ToolSet) GetWorkerTraceTool() actool.CoreTool     { return t.getWorkerTrace() }
func (t *ToolSet) ListWorkerTracesTool() actool.CoreTool   { return t.listWorkerTraces() }
func (t *ToolSet) SearchWorkerTracesTool() actool.CoreTool { return t.searchAllWorkerTraces() }
func (t *ToolSet) NodeDetailTool() actool.CoreTool         { return t.nodeDetail() }
func (t *ToolSet) AddHintTool() actool.CoreTool            { return t.addHint() }

// SetEnrich wires the async enrichment engine (DNS/HTTP auto-completion).
func (t *ToolSet) SetEnrich(e EnrichTrigger) { t.enrich = e }

// SetOwnerNode sets the exploration node that writes anchor to (worker: its
// intent node; planner/main: the begin root). Assets created/referenced while
// ownerNode is set are anchored to it as lineage (not visibility).
func (t *ToolSet) SetOwnerNode(id int64) { t.ownerNode = id }

// anchorOwner records a lineage edge from this run's owner node to an asset
// (no-op if unset). Provenance only — the asset graph is global and shared, so
// this no longer affects which assets a task can read.
func (t *ToolSet) anchorOwner(assetID int64) {
	if t.ts != nil && t.ownerNode > 0 && assetID > 0 {
		_ = t.ts.Anchor(t.ownerNode, assetID)
	}
}

// pid parses an id that may arrive as a JSON number or string ("" / 0 → 0).
func pid(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// pidList parses a list of ids (number|string), dropping zeros/invalids.
func pidList(raw []json.RawMessage) []int64 {
	var out []int64
	for _, r := range raw {
		if v := pid(r); v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func idp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func readTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:    func(json.RawMessage) bool { return true },
		Concurrent:  func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func writeTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// readExpTool / writeExpTool build a domain tool whose handler dereferences the
// task-bound ExplorationStore. Two ToolSets carry a nil store: the catalog's
// seed-only shell (never called) and the server-level one behind buildDomainReg,
// which the tools table can bind to ANY agent — including ones that never run
// inside a task (auto/pentest/reporter/custom agent/side question). Refusing there
// keeps a mis-bound tool a bad tool call; without the guard it was a nil deref,
// and tool handlers run on the harness's own goroutine, so the panic is out of
// reach of every recover() in the server and kills the whole process.
func (t *ToolSet) readExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return readTool(name, desc, schema, t.needExploration(name, run))
}

func (t *ToolSet) writeExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return writeTool(name, desc, schema, t.needExploration(name, run))
}

// needExploration wraps a handler so it only runs with an exploration store.
// Tools that degrade more usefully than "unavailable" (report_finding points at
// add_task_hint, set_goals/set_constraints at the task itself) keep their own
// bespoke guard instead.
func (t *ToolSet) needExploration(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) func(context.Context, json.RawMessage) (actool.Result, error) {
	return func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if t.ts == nil {
			return actool.Errorf(name + " requires a task context (exploration graph): the current agent is not running inside a task, so the task's exploration graph is unavailable and this tool cannot be used. Use it inside a task, or switch to a cross-task read tool that takes task_id (get_task_node_detail / list_task_findings / get_task_graph, etc.)."), nil
		}
		return run(ctx, in)
	}
}

func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// --- read tools (planner + worker) ---

func (t *ToolSet) graphOverview() actool.CoreTool {
	return t.readExpTool("graph_overview",
		"(exploration graph) Distilled summary of the exploration situation: asset counts, sites without endpoints, frontier, findings, hints (strategic hints from humans/the main agent, which must be factored in when generating intents). Call it first when planning.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			return jsonResult(t.graphOverviewData())
		})
}

// graphOverviewData computes the distilled situational snapshot shared by the
// graph_overview tool and the planner's wake-up prompt (which pre-injects it so
// the model needn't spend a turn calling the tool — every plan round starts with
// an empty context and always needs this first).
func (t *ToolSet) graphOverviewData() map[string]any {
	out := map[string]any{}
	out["completion_work_index"] = map[string]any{
		"tool":        "list_remaining_work",
		"instruction": "The overview shows only some intents. Before wrapping up, use list_remaining_work to page through every non-done intent (including paused/stopped/deleted) until has_more=false.",
	}
	// goals summary folded in so the planner needn't call list_goals each round.
	goals, _ := t.ts.ListByKind(db.KindGoal, 100)
	gsum := make([]map[string]any, 0, len(goals))
	for _, g := range goals {
		var p map[string]any
		_ = json.Unmarshal(g.Payload, &p)
		gsum = append(gsum, map[string]any{"id": g.ID, "state": g.State, "text": p["text"]})
	}
	out["goals"] = gsum
	// hints: strategic hints that humans/the main agent attached to the graph via add_hint; folded in so the
	// planner reads them every round when generating intents (otherwise they'd be written but never read).
	hints, _ := t.ts.ListByKind(db.KindHint, 50)
	hsum := make([]map[string]any, 0, len(hints))
	for _, h := range hints {
		var p map[string]any
		_ = json.Unmarshal(h.Payload, &p)
		hint := map[string]any{"id": h.ID, "state": h.State, "text": p["text"]}
		if findingTrafficBindingEnabled() && p["traffic_refs"] != nil {
			hint["traffic_refs"] = p["traffic_refs"]
		}
		hsum = append(hsum, hint)
	}
	out["hints"] = hsum
	// lineage from the exploration edges: an intent's parents (what it
	// derived_from — possibly several facts combined) and its yields (the
	// facts/findings it produced). factFrom maps a fact → the intent that
	// produced it. This is the relationship layer the flat lists lacked.
	edges, _ := t.ts.Edges(5000)
	parentsOf := map[int64][]int64{}
	yieldsOf := map[int64][]int64{}
	factFrom := map[int64]int64{}
	for _, e := range edges {
		switch e.Rel {
		case db.RelDerivedFrom, db.RelSpawns: // upstream: derived_from (fact/finding/intent→intent) or spawns (origin fact→goal, legacy begin→intent)
			parentsOf[e.To] = append(parentsOf[e.To], e.From)
		case db.RelYields: // intent --yields--> fact/finding
			yieldsOf[e.From] = append(yieldsOf[e.From], e.To)
			factFrom[e.To] = e.From
		}
	}
	// cold-digest §6: members folded into an active digest are shown via cold_digests
	// (below), not the flat recent_* lists. `covered` maps member id → its digest id.
	// §6 render-time revival check: a covered member that has become hot again (a new
	// intent derived from it) must reappear this round — so `hidden` folds a member out
	// only when it is covered AND still cold.
	covered, _ := t.ts.CoveredMembers()
	// Render-time hot set (ancestor of a live intent / fact under a live intent).
	// §6 revival check: a covered member that revived (now hot) must NOT stay folded
	// — hidden() only folds a member out when it is covered AND still cold. Computed
	// every round (cheap for real graph sizes); nil map degrades safely.
	var hotAtRender map[int64]bool
	if cg, _, err := loadColdGraph(t.ts); err == nil {
		hotAtRender = cg.hotSet()
	}
	hidden := func(id int64) bool { _, c := covered[id]; return c && !hotAtRender[id] }
	const openIntentsCap = 30
	fr, _ := t.ts.Frontier(openIntentsCap) // priority DESC, id ASC — the top N by priority; real total in frontier_open
	out["open_intents"] = compactIntents(fr, parentsOf, yieldsOf)
	all, _ := t.ts.ListByKind(db.KindIntent, 300)
	var running, recentDone []*db.Node
	for _, n := range all {
		switch n.State {
		case "running":
			running = append(running, n)
		case "done", "blocked", "exhausted":
			if hidden(n.ID) {
				continue // in a cold_digest and still cold — shown via cold_digests (§6.2)
			}
			recentDone = append(recentDone, n) // newest first (all is sorted by id DESC); folded ones are excluded, output is truncated to the newest N
		}
	}
	out["running_intents"] = compactIntents(running, parentsOf, yieldsOf)
	// done_intents_total: total of finished intents (done/blocked/exhausted), named in parallel
	// with recent_done_intents — the latter is just its newest-window truncated view. Side by side the keys are self-describing: "you see N/total",
	// so the planner doesn't mistake "not shown" for "never dispatched" when deduplicating, with no extra explanation needed in the prompt.
	if dt, err := t.ts.CountFinishedIntents(); err == nil {
		out["done_intents_total"] = dt
	}
	// frontier_open: real total of open intents (open_intents is only a truncated view of the top N by priority).
	if fo, err := t.ts.CountOpenIntents(); err == nil {
		out["frontier_open"] = fo
	} else {
		out["frontier_open"] = len(fr)
	}
	// findings (confirmed vulns) and facts (worker exploration results) are
	// now distinct node kinds. recent_facts surfaces fact summaries (esp.
	// negative results) so the planner sees them in one call; full content
	// via node_detail(id).
	vulnNodes, _ := t.ts.ListByKind(db.KindFinding, 1000)
	factNodes, _ := t.ts.ListByKind(db.KindFact, 1000) // newest first
	out["findings_total"] = len(vulnNodes)             // total confirmed findings (goal judgement uses this); details in finding_list (newest window)
	out["facts"] = len(factNodes)                      // number of exploration facts/conclusions (including negative conclusions)
	// findings are the highest-value output of a task → the overview carries the newest window (≤10; vulnNodes is sorted by id DESC, i.e. newest first),
	// so the planner sees recently confirmed findings at a glance when judging goals each round; use list_findings for the full/older set.
	// Each entry keeps only {id, summary, from_intent?}: from_intent is the intent that produced this finding.
	// evidence/assets/vulnclass/severity/state etc. are still available via list_findings / node_detail(id).
	const findingListCap = 10
	findingList := make([]map[string]any, 0, findingListCap)
	for _, n := range vulnNodes {
		if len(findingList) >= findingListCap {
			break
		}
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		m := map[string]any{"id": n.ID, "summary": fp["summary"]}
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // the intent that produced this finding
		}
		findingList = append(findingList, m)
	}
	out["finding_list"] = findingList
	// recent_facts: the newest window (≤N; factNodes sorted by id DESC, newest first) of non-folded facts. Ones already folded into
	// a digest and still cold (hidden) go through cold_digests and are not repeated here. Each entry is {id, summary, from_intent?,
	// confidence?}; details such as evidence via node_detail(id). Page through older ones with list_facts.
	const recentFactsCap = 20
	recentFacts := make([]map[string]any, 0, recentFactsCap)
	for _, n := range factNodes {
		if len(recentFacts) >= recentFactsCap {
			break
		}
		if hidden(n.ID) {
			continue // already folded into a digest and still cold — see cold_digests
		}
		m := compactNode(n)
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // the intent that produced this fact
		}
		// confidence is carried into the overview so the planner can see at a glance which conclusions are only inferred (especially
		// negative conclusions, which should not be treated as settled); evidence is longer and left to node_detail(id).
		var fp map[string]any
		if json.Unmarshal(n.Payload, &fp) == nil {
			if c, ok := fp["confidence"].(string); ok && c != "" {
				m["confidence"] = c
			}
		}
		recentFacts = append(recentFacts, m)
	}
	out["recent_facts"] = recentFacts
	// recent_done_intents: the newest window (≤N; recentDone already sorted by id DESC) of non-folded finished intents.
	// For older ones see the done_intents_total count + node_detail(id).
	const recentDoneCap = 12
	if len(recentDone) > recentDoneCap {
		recentDone = recentDone[:recentDoneCap]
	}
	out["recent_done_intents"] = compactIntents(recentDone, parentsOf, yieldsOf)
	// cold-digest §6.1: digest bodies of the folded cold zone, top N by newest member time DESC; older truncated digests
	// get only bare ids (still expandable via expand_digest), so the cold zone's only outlet doesn't grow without bound.
	const coldDigestsCap = 15
	if cds, more := coldDigestsRecent(t.ts, coldDigestsCap); len(cds) > 0 {
		out["cold_digests"] = cds // [{id, body, member_count}] — read body directly (§6.1)
		if len(more) > 0 {
			out["cold_digests_more"] = more // ids of older truncated digests; expand with expand_digest(id)
		}
	}
	// the original task (root) so the planner always has it, not just the
	// decomposed goals.
	if description, goal, err := t.ts.Root(); err == nil {
		out["task"] = map[string]any{"description": description, "goal": goal}
	}
	// Direct source tasks are a live, read-only blackboard view. Keep their
	// summaries in a separate field so their intents never enter this task's
	// frontier or get mistaken for locally claimable work.
	out["related_tasks"] = t.relatedTaskOverviews()
	// coverage: a rough reference for asset test coverage — among assets in scope (task_scope), the share touched by a fact
	// + by_type (total/tested per type). To see the specific untested assets, the agent calls list_untested_assets on demand and judges for itself. Task context only.
	// When asset coverage is disabled (coverageDisabled): keep only host_count (awareness of the target host count),
	// drop coverage metrics such as denominator/tested/pct/by_type/note, to avoid polluting the context and to avoid nudging toward
	// the hidden add_task_scope/list_untested_assets.
	if t.as != nil && t.ts != nil && t.taskID > 0 {
		{
			m := map[string]any{}
			if scope, err := t.taskScopeContext(); err == nil {
				m["scope"] = scope
			}
			if !t.coverageDisabled {
				if cov, err := t.as.TaskCoverageWithSources(t.taskID); err == nil {
					m["denominator"] = cov.Denominator
					m["tested"] = cov.Tested
					m["by_type"] = cov.ByType
					m["note"] = "coverage: asset test coverage (including endpoints and other related assets), a rough estimate for reference only. It covers the scope and fact anchors of the current task and directly linked tasks; linked scope is read-only. Container-type assets or large enumerations make it low, so do not take it to mean testing is complete. Use add_task_scope to extend this task's scope and list_untested_assets to see untested assets [usually do not call list_untested_assets; just proceed with the task];"
					if cov.Denominator == 0 {
						m["pct"] = nil
						m["status"] = "scope not anchored"
					} else {
						m["pct"] = cov.Pct
					}
				}
			}
			if hosts, err := t.as.HostsByTaskWithSources(t.taskID); err == nil {
				// Only give the total host count instead of flattening the host list into graph_overview (in large-scope tasks that is a
				// large set of strings repeated every round, of limited value for planning decisions); query specific hosts with list_assets on demand.
				m["host_count"] = len(hosts)
			}
			if len(m) > 0 {
				out["coverage"] = m
			}
		}
	}
	return out
}

func inheritedMap(m map[string]any, sourceTaskID int64) map[string]any {
	m["source_task_id"] = sourceTaskID
	m["inherited"] = true
	return m
}

const (
	relatedOverviewTotalTextRunes      = 48_000
	relatedOverviewMaxTextPerSource    = 8_000
	relatedOverviewMaxGoalsPerSource   = 8
	relatedOverviewMaxHintsPerSource   = 6
	relatedOverviewMaxFactsPerSource   = 12
	relatedOverviewMaxFindingsPerTask  = 6
	relatedOverviewMaxIntentsPerTask   = 8
	relatedOverviewMaxScopePerSource   = 12
	relatedOverviewMaxDigestsPerSource = 6
)

// overviewTextBudget bounds inherited prompt text while preserving a fair slice
// for every direct source. Full evidence remains available through the on-demand
// read tools, so truncation here does not discard persisted blackboard data.
type overviewTextBudget struct {
	remaining int
	truncated bool
}

func relatedOverviewBudgetForSources(sourceCount int) int {
	if sourceCount <= 0 {
		return 0
	}
	if sourceCount > db.MaxTaskSourceCount {
		sourceCount = db.MaxTaskSourceCount
	}
	perSource := relatedOverviewTotalTextRunes / sourceCount
	if perSource > relatedOverviewMaxTextPerSource {
		perSource = relatedOverviewMaxTextPerSource
	}
	return perSource
}

func (b *overviewTextBudget) take(value any, fieldLimit int) string {
	var text string
	switch value := value.(type) {
	case string:
		text = strings.TrimSpace(value)
	case nil:
		return ""
	default:
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "" {
		return ""
	}
	if b.remaining <= 0 || fieldLimit <= 0 {
		b.truncated = true
		return ""
	}
	runes := []rune(text)
	limit := fieldLimit
	if limit > b.remaining {
		limit = b.remaining
	}
	if len(runes) > limit {
		b.truncated = true
		if limit == 1 {
			text = "…"
		} else {
			text = string(runes[:limit-1]) + "…"
		}
		runes = []rune(text)
	}
	b.remaining -= len(runes)
	return text
}

func recentTerminalIntents(store *db.ExplorationStore, limit int) []*db.Node {
	if limit <= 0 {
		return []*db.Node{}
	}
	const batch = 300
	cursor := int64(0)
	out := make([]*db.Node, 0, limit)
	for len(out) < limit {
		page, more, err := store.ListByKindPage(db.KindIntent, cursor, batch)
		if err != nil || len(page) == 0 {
			break
		}
		for _, intent := range page {
			switch intent.State {
			case "done", "blocked", "exhausted", "stopped":
				out = append(out, intent)
			}
			if len(out) >= limit {
				break
			}
		}
		if !more {
			break
		}
		cursor = page[len(page)-1].ID
	}
	return out
}

// relatedTaskOverviews distills persistent blackboard state from direct source
// tasks. It intentionally reads each source's local store methods, never its own
// related sources, so inheritance is one level only.
func (t *ToolSet) relatedTaskOverviews() []map[string]any {
	sources, err := t.ts.DirectSourceStores()
	if err != nil {
		return []map[string]any{}
	}
	if len(sources) > db.MaxTaskSourceCount {
		sources = sources[:db.MaxTaskSourceCount]
	}
	perSourceTextBudget := relatedOverviewBudgetForSources(len(sources))
	out := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		ts := source.Store
		// §2 cross-task: render the source task's OWN folded view — fold out the
		// members it has already folded, and surface its cold_digests read-only.
		hidden := hiddenMembersFor(ts)
		budget := overviewTextBudget{remaining: perSourceTextBudget}
		item := map[string]any{
			"source_task_id": source.Task.TaskID,
			"inherited":      true,
			"task": map[string]any{
				"description": budget.take(source.Task.Description, 800),
				"goal":        budget.take(source.Task.Goal, 800),
				"status":      source.Task.Status,
			},
		}
		stats, statsErr := ts.Stats()

		edges, _ := ts.Edges(5000)
		parentsOf := map[int64][]int64{}
		yieldsOf := map[int64][]int64{}
		factFrom := map[int64]int64{}
		for _, edge := range edges {
			switch edge.Rel {
			case db.RelDerivedFrom, db.RelSpawns:
				parentsOf[edge.To] = append(parentsOf[edge.To], edge.From)
			case db.RelYields:
				yieldsOf[edge.From] = append(yieldsOf[edge.From], edge.To)
				factFrom[edge.To] = edge.From
			}
		}

		goals, _ := ts.ListByKind(db.KindGoal, relatedOverviewMaxGoalsPerSource)
		goalSummary := make([]map[string]any, 0, len(goals))
		for _, goal := range goals {
			var payload map[string]any
			_ = json.Unmarshal(goal.Payload, &payload)
			goalSummary = append(goalSummary, inheritedMap(map[string]any{
				"id": goal.ID, "state": goal.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["goals"] = goalSummary

		hints, _ := ts.ListByKind(db.KindHint, relatedOverviewMaxHintsPerSource)
		hintSummary := make([]map[string]any, 0, len(hints))
		for _, hint := range hints {
			var payload map[string]any
			_ = json.Unmarshal(hint.Payload, &payload)
			hintSummary = append(hintSummary, inheritedMap(map[string]any{
				"id": hint.ID, "state": hint.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["hints"] = hintSummary

		facts, _ := ts.ListByKind(db.KindFact, relatedOverviewMaxFactsPerSource)
		findings, _ := ts.ListByKind(db.KindFinding, relatedOverviewMaxFindingsPerTask)
		intentNodes, _ := ts.ListByKind(db.KindIntent, 300)
		terminalIntent := make(map[int64]bool, len(intentNodes))
		for _, intent := range intentNodes {
			terminalIntent[intent.ID] = inheritedIntentSummaryState(intent.State)
		}
		item["facts"] = len(facts)
		item["findings"] = len(findings)
		if statsErr == nil {
			item["facts"] = stats[db.KindFact]
			item["findings"] = stats[db.KindFinding]
			if stats[db.KindGoal] > len(goals) || stats[db.KindHint] > len(hints) ||
				stats[db.KindFact] > len(facts) || stats[db.KindFinding] > len(findings) {
				budget.truncated = true
			}
		}
		recentFindings := make([]map[string]any, 0, len(findings))
		for _, finding := range findings {
			entry := inheritedMap(compactFinding(finding), source.Task.TaskID)
			entry["summary"] = budget.take(entry["summary"], 400)
			recentFindings = append(recentFindings, entry)
		}
		item["recent_findings"] = recentFindings
		recentFacts := make([]map[string]any, 0, len(facts))
		for _, fact := range facts {
			if hidden(fact.ID) {
				continue // folded into this source's cold_digests — shown there (§2/§6.2)
			}
			m := inheritedMap(compactNode(fact), source.Task.TaskID)
			m["summary"] = budget.take(m["summary"], 400)
			if from := factFrom[fact.ID]; from > 0 && terminalIntent[from] {
				m["from_intent"] = from
			}
			var payload map[string]any
			if json.Unmarshal(fact.Payload, &payload) == nil {
				if confidence, ok := payload["confidence"].(string); ok && confidence != "" {
					m["confidence"] = confidence
				}
			}
			recentFacts = append(recentFacts, m)
		}
		item["recent_facts"] = recentFacts

		recentDoneRaw := recentTerminalIntents(ts, relatedOverviewMaxIntentsPerTask)
		recentDone := recentDoneRaw[:0] // in-place filter: drop this source's folded intents (§2)
		for _, intent := range recentDoneRaw {
			if hidden(intent.ID) {
				continue
			}
			recentDone = append(recentDone, intent)
		}
		for _, intent := range recentDone {
			intent.Inherited = true
			intent.SourceTaskID = source.Task.TaskID
		}
		intentResults := compactIntents(recentDone, parentsOf, yieldsOf)
		for i, intent := range recentDone {
			intentResults[i]["summary"] = budget.take(intentResults[i]["summary"], 400)
			acts, _, err := ts.ActivityPageForTerminalIntent(intent.ID, 0, 20)
			if err != nil {
				continue
			}
			var resultSummary, textFallback string
			for _, activity := range acts {
				switch activity.Kind {
				case "result":
					resultSummary = activity.Summary
				case "text":
					textFallback = activity.Summary
				}
			}
			if resultSummary == "" {
				resultSummary = textFallback
			}
			if resultSummary != "" {
				intentResults[i]["result_summary"] = budget.take(resultSummary, 800)
			}
		}
		item["recent_intent_results"] = intentResults
		// §2 cross-task: the source task's folded cold region, read-only, newest-member
		// first & capped like the current task's. Members (and overflow digests) are
		// resolvable via expand_digest(id)/node_detail(id), which search source tasks.
		if cds, more := coldDigestsRecent(ts, relatedOverviewMaxDigestsPerSource); len(cds) > 0 {
			for _, cd := range cds {
				cd["inherited"] = true
				cd["source_task_id"] = source.Task.TaskID
			}
			item["cold_digests"] = cds
			if len(more) > 0 {
				item["cold_digests_more"] = more // ids of older truncated digests; expand with expand_digest(id)
			}
		}
		if statsErr == nil {
			item["node_stats"] = stats
		}

		if t.as != nil {
			if scopeRows, err := t.as.ListTaskScope(source.Task.TaskID); err == nil && len(scopeRows) > 0 {
				scopeCount := len(scopeRows)
				if len(scopeRows) > relatedOverviewMaxScopePerSource {
					scopeRows = scopeRows[:relatedOverviewMaxScopePerSource]
					budget.truncated = true
				}
				scope := make([]map[string]any, 0, len(scopeRows))
				for _, row := range scopeRows {
					entry := map[string]any{"kind": row.Kind, "source": budget.take(row.Source, 300)}
					switch {
					case row.Domain != "":
						entry["value"] = budget.take(row.Domain, 400)
					case row.Net != "":
						entry["value"] = budget.take(row.Net, 400)
					case row.Value != "":
						entry["value"] = budget.take(row.Value, 400)
					case row.CompanyID != nil:
						entry["company_id"] = *row.CompanyID
					}
					scope = append(scope, entry)
				}
				item["asset_scope"] = scope
				item["asset_scope_count"] = scopeCount
			}
			if coverage, err := t.as.TaskCoverage(source.Task.TaskID, source.Task.ExplorationID); err == nil {
				item["asset_coverage"] = map[string]any{
					"denominator": coverage.Denominator,
					"tested":      coverage.Tested,
					"pct":         coverage.Pct,
					"by_type":     coverage.ByType,
				}
			}
		}
		if budget.truncated {
			item["summary_truncated"] = true
		}
		out = append(out, item)
	}
	return out
}

func inheritedIntentSummaryState(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// compactNode distills any exploration node to id + summary + state, dropping the
// big detail/evidence (fetch that on demand via node_detail).
func compactNode(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	return m
}

// compactFinding is compactNode plus the vuln-specific vulnclass/severity.
func compactFinding(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	if vc, ok := p["vulnclass"]; ok && vc != nil && vc != "" {
		m["vulnclass"] = vc
	}
	if sv, ok := p["severity"]; ok && sv != nil && sv != "" {
		m["severity"] = sv
	}
	return m
}

func (t *ToolSet) listFindings() actool.CoreTool {
	return t.readExpTool("list_findings", "List [confirmed findings] of this task and directly linked tasks (compact: id+task_id+intent_id+vulnclass+severity+summary+state). Entries from linked tasks carry source_task_id/inherited=true and are read-only. Only findings are included here; use list_facts for ordinary exploration facts and node_detail(id) for details.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			f, _ := t.ts.ListByKindWithSources(db.KindFinding, 500)
			if err := t.ts.PopulateFindingTrafficIDs(f); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			intentOf, _ := t.ts.FindingIntentsWithSources() // finding id -> the intent id that produced it
			taskID := t.taskID
			if taskID <= 0 {
				taskID, _ = t.ts.TaskID()
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				m := compactFinding(n)
				if n.FindingID > 0 {
					m["finding_id"], m["finding_node_id"], m["traffic_count"] = n.FindingID, n.ID, n.TrafficCount
				}
				if n.Inherited {
					m["task_id"] = n.SourceTaskID
				} else {
					m["task_id"] = taskID
				}
				if iid, ok := intentOf[n.ID]; ok {
					m["intent_id"] = iid
				}
				out = append(out, m)
			}
			return jsonResult(out)
		})
}

// factsPageSize is the default page size for list_facts. Facts pile up on long
// tasks; returning all of them at once (the old behaviour) could blow up the
// context, so default to the newest page and let the agent page/filter for more.
const factsPageSize = 20

func (t *ToolSet) listFacts() actool.CoreTool {
	return t.readExpTool("list_facts", "Page through [exploration facts/conclusions] of this task and directly linked tasks, newest first (compact: id+summary+state; long summaries are truncated, use node_detail(id) for the full text). All parameters optional: limit (default 20, max 100), before (cursor: pass the next_before from the previous page to get an older page; omitted/0 = newest page), q (filter by summary keyword). Returns {facts, total, has_more, next_before}: total is the filtered total; when has_more=true continue paging with next_before. Entries from linked tasks carry source_task_id/inherited=true and are read-only. For findings see list_findings.",
		obj(map[string]any{
			"limit":  intp("number of entries to return, default 20, max 100"),
			"before": intp("paging cursor: return only older facts with id below this value; omitted or 0 = newest page"),
			"q":      str("filter by fact summary keyword (case-insensitive); omitted = no filter"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Limit  int    `json:"limit"`
				Before int64  `json:"before"`
				Q      string `json:"q"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = factsPageSize
			}
			if limit > 100 {
				limit = 100
			}
			f, hasMore, total, err := t.ts.ListByKindPageWithSources(db.KindFact, a.Before, limit, strings.TrimSpace(a.Q))
			if err != nil {
				return actool.Result{}, err
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				out = append(out, compactFact(n))
			}
			res := map[string]any{"facts": out, "total": total, "has_more": hasMore}
			if hasMore && len(f) > 0 {
				res["next_before"] = f[len(f)-1].ID // pass it back to get the next (older) page
			}
			return jsonResult(res)
		})
}

// factSummaryMax caps a fact summary in list_facts output. Facts carry one-line
// conclusions, but nothing enforces brevity; a runaway summary must not bloat a
// whole page. Full text stays available via node_detail(id).
const factSummaryMax = 160

// compactFact is compactNode with the summary rune-capped for list_facts, so a
// page of facts stays bounded regardless of how long any single summary grew.
func compactFact(n *db.Node) map[string]any {
	m := compactNode(n)
	if s, ok := m["summary"].(string); ok && len([]rune(s)) > factSummaryMax {
		m["summary"] = string([]rune(s)[:factSummaryMax]) + "…"
		m["summary_truncated"] = true
	}
	return m
}

func (t *ToolSet) nodeDetail() actool.CoreTool {
	return t.readExpTool("node_detail", "Get the full content of an [exploration graph node] of this task or a directly linked task by id. Inherited nodes carry source_task_id/inherited=true and are read-only. Only for exploration node ids returned by list_facts/list_findings/graph_overview; for assets use list_assets/asset_neighbors.",
		obj(map[string]any{"id": idp("exploration graph node id (not an asset id)")}, "id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.ID)
			if id <= 0 {
				return actool.Errorf("id is required"), nil
			}
			n, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil {
				return actool.Errorf(fmt.Sprintf("exploration node %d not found. If you are looking for an asset, use list_assets / asset_neighbors (assets and exploration nodes are separate id spaces; asset ids cannot be passed to node_detail).", id)), nil
			}
			if err := t.ts.PopulateFindingTrafficIDs([]*db.Node{n}); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(n) // full payload incl. detail / evidence, plus explicit finding IDs
		})
}

// --- planner write tools ---

// intentItem is one exploration direction of an add_intent batch/single call.
type intentItem struct {
	Check          *db.CheckSpec     `json:"check"`
	RetryOf        int64             `json:"retry_of"`
	RecoveryChange string            `json:"recovery_change"`
	Summary        string            `json:"summary"`
	AssetIDs       []json.RawMessage `json:"asset_ids"`
	ParentIDs      []json.RawMessage `json:"parent_ids"`
	Priority       int               `json:"priority"`
}

// addOneIntent creates one intent node, links its upstream lineage and returns its id.
// Constraint: an intent may only be anchored on confirmed knowledge — every parent_id must be an existing fact/finding
// node (not another intent/goal/hint). Brand-new top-level directions leave parent_ids empty and fall back to the origin fact.
// This way "every intent is linked to a fact node and is discovery-driven rather than planned out of thin air" is enforced on the creation path.
func (t *ToolSet) addOneIntent(ctx context.Context, it intentItem) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary must not be empty")
	}
	if it.Check == nil && it.RetryOf == 0 {
		strict, err := t.ts.HasGoalContract()
		if err != nil {
			return 0, err
		}
		if strict {
			return 0, fmt.Errorf("the current goal has a saved check contract; new work must provide a stable check.target/mechanism")
		}
	}
	// Validate anchors first (before creating the node, so bad anchors don't leave orphan intents).
	parents := pidList(it.ParentIDs)
	for _, pidv := range parents {
		n, err := t.ts.GetNodeWithSources(pidv)
		if err != nil || n == nil {
			return 0, fmt.Errorf("parent_id %d does not exist in this task or a directly linked task: parent_ids must be ids of existing [fact/finding] nodes; leave parent_ids empty for a brand-new top-level direction", pidv)
		}
		if n.Kind != db.KindFact && n.Kind != db.KindFinding {
			return 0, fmt.Errorf("parent_id %d is a %q node and cannot anchor an intent: intents may only be anchored on confirmed [facts/findings], not on intents/goals/hints; leave parent_ids empty for a brand-new top-level direction", pidv, n.Kind)
		}
	}
	priority := it.Priority
	if priority == 0 {
		priority = 5
	}
	anchors := pidList(it.AssetIDs)
	// Asset intercept: if an asset bound to the intent hits a system asset intercept rule, dispatching the intent is forbidden.
	if t.as != nil && len(anchors) > 0 {
		hits, err := t.as.CheckAssetsIntercept(t.taskID, anchors)
		if err != nil {
			return 0, fmt.Errorf("asset intercept check failed: %w", err)
		}
		if len(hits) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "assets bound to intent \"%s\" failed the test scope check; stop testing the affected assets: ", it.Summary)
			for _, h := range hits {
				fmt.Fprintf(&b, "\n - %s", h.Describe())
			}
			return 0, fmt.Errorf("%s", b.String())
		}
	}
	payload := map[string]any{"summary": it.Summary}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	var id int64
	var err error
	if it.Check != nil || it.RetryOf > 0 {
		check := db.CheckSpec{}
		if it.Check != nil {
			check = *it.Check
		}
		id, err = t.ts.AddCheckIntent(ctx, payload, priority, anchors, "planner", check, it.RetryOf, it.RecoveryChange)
	} else {
		id, err = t.ts.AddIntent(payload, priority, anchors, "planner")
	}
	if err != nil {
		return 0, err
	}
	// upstream lineage: link each (validated) fact/finding parent → this intent, so
	// "multiple facts combine into one new intent" is expressible.
	for _, parent := range parents {
		_ = t.ts.Link(parent, db.RelDerivedFrom, id)
	}
	// a top-level intent (no explicit parent) connects to the origin fact, so every
	// intent still traces back to a fact node — at task start the only fact is the
	// origin, and the first intents derive from it.
	if len(parents) == 0 {
		if origin, _ := t.ts.OriginFactID(); origin > 0 {
			_ = t.ts.Link(origin, db.RelDerivedFrom, id)
		}
	}
	return id, nil
}

func (t *ToolSet) addIntent() actool.CoreTool {
	return t.writeExpTool("add_intent", "Generate [exploration directions], write them to the frontier and link them into the exploration graph. An intent is an open exploration direction, not a fixed type — use summary to freely describe in one sentence what to explore/verify/exploit.\n"+
		"★Prefer batches: put the several new directions picked in one round into the intents array and submit once (saves round trips versus one call each). Returns an ids array of the same length and order as intents (failed items have id=0, details in errors). For a single one, omit intents and give a top-level summary.",
		obj(map[string]any{
			"intents":         map[string]any{"type": "array", "description": "[Prefer this] Array of exploration directions to add, processed in order. Each element has the same fields as the top-level fields below (summary/asset_ids/parent_ids/priority). Returned ids have the same length and order as this array.", "items": map[string]any{"type": "object"}},
			"summary":         str("[single] One sentence describing this exploration direction: what to do + why. A clearly stated direction is enough; it does not depend on asset ids."),
			"asset_ids":       map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "[Target asset ids] this direction tests/attacks (**pass them whenever possible**; 0/1/many; these are asset ids returned by list_assets, not exploration node ids): which assets (sites/endpoints/parameters/hosts, etc.) this direction targets. Whenever the direction revolves around specific assets, be sure to pass them — this is the structured marker of \"which targets this exploration hits\", used for coverage dedup and to link the intent into the asset graph. Leave empty only for purely global recon with genuinely no specific target asset."},
			"parent_ids":      map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Upstream anchor ids (optional, 0/1/many): which [confirmed facts/findings] this direction was synthesized from. **Only ids of existing fact/finding nodes; never intents/goals/hints** — intents must be anchored on confirmed knowledge, discovery-driven rather than planned out of thin air. Pass several when multiple facts jointly produce one new intent; leave empty for a brand-new top-level recon direction (it is attached automatically to the task's origin fact)."},
			"priority":        intp("priority 0-10, default 5"),
			"check":           obj(map[string]any{"target": str("stable normalized target/endpoint"), "mechanism": str("stable check mechanism; do not rename when rebuilding")}, "target", "mechanism"),
			"retry_of":        idp("ID of the original intent being recovered"),
			"recovery_change": str("the condition changed / fix applied this time; required for repeated checks"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Intents    []intentItem `json:"intents"`
				intentItem              // single mode: top-level summary/asset_ids/parent_ids/priority
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Intents) > 0
			items := a.Intents
			if !batch {
				items = []intentItem{a.intentItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			createdAny := false
			for i, it := range items {
				id, err := t.addOneIntent(ctx, it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				createdAny = true
			}

			// Human-directed intent via the main agent → if the task is already done (goalless branch with no open goals), pull it
			// back to running so a worker can pick up this intent. resumeTask is wired only by the main agent's Chat
			// (SetResumeTask); the planner's ToolSet has it nil, so when the planner itself calls add_intent this block is a
			// no-op and doesn't affect normal intent production. The intent node was already created above (open), so revival won't misjudge the frontier as drained.
			if createdAny && t.resumeTask != nil {
				t.resumeTask()
			}

			if !batch { // single: keep the original return
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("intent created: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) listGoals() actool.CoreTool {
	return t.readExpTool("list_goals", "List this task's goal nodes and their states (open/met), to judge whether they are achieved.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			g, _ := t.ts.ListByKind(db.KindGoal, 100)
			return jsonResult(g)
		})
}

func (t *ToolSet) proveGoal() actool.CoreTool {
	return t.writeExpTool("prove_goal", "Submit a goal of this task with evidence for independent review. When the saved evidence really supports the outcome or the specified check, the reason and proof edge are saved atomically and the goal is marked met; a negative check cannot prove an outcome was obtained.",
		obj(map[string]any{
			"goal_id":        idp("goal node id"),
			"evidence_id":    idp("id of the finding/fact node that proves it"),
			"reason":         str("why this evidence satisfies the goal"),
			"remaining_work": completionDispositionSchema(),
		}, "goal_id", "evidence_id", "reason"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				GoalID     json.RawMessage            `json:"goal_id"`
				EvidenceID json.RawMessage            `json:"evidence_id"`
				Reason     string                     `json:"reason"`
				Remaining  []db.CompletionDisposition `json:"remaining_work"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid parameters"), nil
			}
			goal, ev := pid(a.GoalID), pid(a.EvidenceID)
			if goal == 0 || ev == 0 || strings.TrimSpace(a.Reason) == "" {
				return actool.Errorf("goal_id, evidence_id and a non-empty reason are required"), nil
			}
			if t.goalReviewer == nil {
				return actool.Errorf("the goal evidence review model is not ready yet; cannot mark the goal met"), nil
			}
			snapshot, err := t.ts.GoalProofSnapshot(ctx, goal, ev)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			verdict, err := t.goalReviewer(ctx, snapshot, a.Reason)
			if err != nil {
				return actool.Errorf("goal evidence review failed; verdict not changed: " + err.Error()), nil
			}
			if verdict.Supported == nil || strings.TrimSpace(verdict.Reason) == "" || (verdict.GoalType != "outcome" && verdict.GoalType != "inspection") {
				return actool.Errorf("goal evidence review returned no valid verdict"), nil
			}
			var contract struct {
				Type string `json:"goal_type"`
			}
			if json.Unmarshal(snapshot.Goal.Payload, &contract) != nil || (contract.Type != "" && contract.Type != verdict.GoalType) {
				return actool.Errorf("review cannot change the saved goal type"), nil
			}
			if err = t.ts.CommitGoalProof(ctx, snapshot, *verdict.Supported, t.worker, verdict.Reason); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if !*verdict.Supported {
				t.completionResultMu.Lock()
				t.GoalMet = false
				t.Reason = "goal evidence insufficient; required work must continue"
				t.completionResultMu.Unlock()
				return actool.Errorf("the evidence cannot support the goal being met: " + verdict.Reason + "; this review did not mark the goal met and required work must continue. An incorrect existing met can be corrected with reopen_goal."), nil
			}
			// This is a completion proposal; the independent review commits status.
			t.completionResultMu.Lock()
			t.GoalMet = false
			t.completionResultMu.Unlock()
			if counts, err := t.ts.GoalStateCounts(ctx); err == nil && counts.Total > 0 {
				if counts.Total == counts.Met {
					if t.completionProposal != nil {
						t.completionProposal.setRemaining(a.Remaining)
					}
					t.completionResultMu.Lock()
					t.GoalMet = true
					t.Reason = fmt.Sprintf("all %d goals are met (last triggered by goal %d)", counts.Total, goal)
					t.completionResultMu.Unlock()
					return actool.Text(fmt.Sprintf("goal %d marked met; all goals of this task are met, but you must still verify that every required check is complete; for remaining duplicate/irrelevant intents explain the reason for stopping each one via prove_goal.remaining_work (may be called again on a goal already met)", goal)), nil
				}
			}
			return actool.Text(fmt.Sprintf("goal %d marked met", goal)), nil
		})
}

func (t *ToolSet) goalMet() actool.CoreTool {
	return writeTool("goal_met", "Request task wrap-up. All goals must be proven met and required checks must be complete. Explain for each remaining non-done intent why it is a duplicate or unrelated to the goals; the system wraps up first, then runs a read-only final verification and submits. Errors, exhausted budget or being blocked are not valid reasons to stop required checks.",
		obj(map[string]any{"reason": str("basis that all goals are met and required checks are complete"),
			"remaining_work": map[string]any{"type": "array", "items": obj(map[string]any{
				"intent_id": idp("remaining intent ID"), "kind": map[string]any{"type": "string", "enum": []string{"duplicate", "irrelevant"}},
				"reason": str("explicit basis for stopping this check"), "reference_id": idp("required for duplicate: ID of the completed intent/fact/finding"),
			}, "intent_id", "kind", "reason")}}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Reason    string
				Remaining []db.CompletionDisposition `json:"remaining_work"`
			}
			if err := json.Unmarshal(in, &a); err != nil || strings.TrimSpace(a.Reason) == "" {
				return actool.Errorf("reason is required and remaining_work must be well-formed"), nil
			}
			if t.completionProposal != nil {
				t.completionProposal.setRemaining(a.Remaining)
			}
			t.completionResultMu.Lock()
			t.GoalMet = true
			t.Reason = a.Reason
			t.completionResultMu.Unlock()
			return actool.Text("wrap-up requested; task completion not yet submitted"), nil
		})
}

// --- worker write tools ---

func (t *ToolSet) addFinding() actool.CoreTool {
	return writeTool("report_finding", "Record a confirmed finding, using evidence to supply verifiable proof such as command output and logs. In a task context pass the current intent_id. The returned finding_id is the standalone finding record ID; finding_node_id is the exploration node ID (the first line keeps that node number).", obj(map[string]any{
		"vulnclass": str("vulnerability class"), "name": str("finding name"), "severity": str("critical|high|medium|low"), "summary": str("finding summary"),
		"intent_id": idp("intent id of the current task"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "affected asset ids"},
		"evidence":         str("evidence/PoC text"),
		"evidence_hint_id": idp("optional: ID of the hint node in this task that corresponds to this finding; its structured traffic_refs are carried automatically. Cannot reference inherited hints or hints for other findings"),
		"traffic_refs": map[string]any{"type": "array", "description": "Optional. For HTTP/HTTPS findings, first search and verify one by one that the request/response really supports the finding, then fill in the real IDs in reproduction order. For non-HTTP findings such as TCP, when traffic was not captured or no exact record can be found, omit it or pass []; this does not block reporting — explain why in evidence and provide other verifiable evidence. Do not guess IDs, infer association from domain/time, or re-probe just to capture packets. Roles: baseline normal control / proof finding proof / verification additional verification / supporting supporting evidence.",
			"items": obj(map[string]any{"traffic_id": str("real traffic ID returned by traffic_search"), "role": map[string]any{"type": "string", "enum": []string{"baseline", "proof", "verification", "supporting"}}, "note": str("how this traffic supports the finding")}, "traffic_id")},
	}, "vulnclass", "severity", "summary"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		var a struct {
			VulnClass, Name, Severity, Summary, Evidence string
			IntentID                                     json.RawMessage   `json:"intent_id"`
			AssetIDs                                     []json.RawMessage `json:"asset_ids"`
			TrafficRefs                                  []db.TrafficRef   `json:"traffic_refs"`
			EvidenceHintID                               json.RawMessage   `json:"evidence_hint_id"`
		}
		if err := json.Unmarshal(in, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.ts == nil {
			return actool.Errorf("report_finding requires a task context; in platform conversations hand the finding over to the relevant task via add_task_hint, carrying any existing traffic_refs in the hint, and let the task agent record it. Recorded findings can be bound later with bind_finding_traffic."), nil
		}
		// Auto-binding off: ignore the evidence params instead of rejecting the call.
		// stripTrafficParameters already removes them from the advertised schema, but
		// models routinely emit fields anyway — failing here would discard a confirmed
		// finding over a stray parameter. The success path below reports evidence_status
		// "not_bound" with the "disabled, can be linked manually on the page" note, which is what the caller needs.
		if !findingTrafficBindingEnabled() {
			a.TrafficRefs, a.EvidenceHintID = nil, nil
		}
		if len(a.EvidenceHintID) > 0 && pid(a.EvidenceHintID) <= 0 {
			return actool.Errorf("evidence_hint_id must be a valid hint node ID; omit it when there is no handoff hint"), nil
		}
		refs, err := t.findingRefsFromHint(pid(a.EvidenceHintID), a.TrafficRefs)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		input := db.RecordFindingInput{TaskID: t.taskID, ExplorationID: t.ts.ID(), IntentID: pid(a.IntentID), VulnClass: a.VulnClass, Name: a.Name, Severity: a.Severity, Summary: a.Summary, Evidence: a.Evidence, Worker: t.worker, AssetIDs: pidList(a.AssetIDs)}
		var recorded *db.RecordedFinding
		if t.findingRecorder != nil {
			recorded, err = t.findingRecorder.Record(ctx, input, refs)
		} else if len(refs) > 0 {
			return actool.Errorf("traffic evidence store unavailable; finding not recorded"), nil
		} else {
			recorded, err = t.ts.RecordFinding(ctx, input)
		}
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.notifyFinding != nil {
			iid := input.IntentID
			if iid <= 0 {
				iid = t.ownerNode
			}
			t.notifyFinding(iid, a.Summary)
		} else if t.notify != nil {
			t.notify()
		}
		t.writes.Findings++
		// Keep the first line's node-ID contract for existing reporter triggers.
		for i := range recorded.Traffic.Bindings {
			recorded.Traffic.Bindings[i].Snapshot.ReqHead = ""
			recorded.Traffic.Bindings[i].Snapshot.RespHead = ""
		}
		result := struct {
			*db.RecordedFinding
			EvidenceStatus     string `json:"evidence_status"`
			EvidenceNote       string `json:"evidence_note,omitempty"`
			EvidenceAssessment string `json:"evidence_assessment"`
		}{RecordedFinding: recorded, EvidenceStatus: "bound"}
		result.EvidenceAssessment = "needs_review"
		if strings.TrimSpace(a.Evidence) == "" {
			result.EvidenceAssessment = "needs_evidence"
		}
		if len(recorded.Traffic.Bindings) == 0 {
			result.EvidenceStatus = "not_bound"
			result.EvidenceNote = "Finding saved without bound traffic. TCP/no-packet cases can continue normally; if you already have verified HTTP traffic, bind it with an available bind_finding_traffic or on the finding page, then complete the evidence handoff. Do not create the finding again."
			if !findingTrafficBindingEnabled() {
				result.EvidenceNote = "Finding saved. Agent automatic traffic binding is disabled; traffic can be linked manually on the page."
			}
		}
		raw, _ := json.Marshal(result)
		return actool.Text(fmt.Sprintf("finding recorded: %d\n%s", recorded.NodeID, raw)), nil
	})
}

// recordFact writes a general exploration RESULT/conclusion (not a vuln, not a
// new asset) into the EXPLORATION graph, chained to the intent that produced it.
// This is the home for observations and — importantly — negative results
// ("port closed", "param not injectable", "no login found"). Such conclusions
// must NOT be stuffed into the asset graph via upsert_asset.
// factItem is one fact of a record_fact batch/single call.
type factItem struct {
	Checkpoint *db.Checkpoint    `json:"checkpoint"`
	Summary    string            `json:"summary"`
	Detail     string            `json:"detail"`
	Evidence   string            `json:"evidence"`   // one line of key evidence (command + key output line) supporting the conclusion, for later verification
	Confidence string            `json:"confidence"` // observed (seen directly) | inferred (deduced from symptoms)
	IntentID   json.RawMessage   `json:"intent_id"`
	AssetIDs   []json.RawMessage `json:"asset_ids"`
}

// recordOneFact writes one fact node and links it to the intent (intent→yields→fact). defaultIntent is
// the default intent for a batch (used when this entry has no intent_id).
func (t *ToolSet) recordOneFact(ctx context.Context, it factItem, defaultIntent int64) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary must not be empty")
	}
	payload := map[string]any{"summary": it.Summary}
	if it.Detail != "" {
		payload["detail"] = it.Detail
	}
	if e := strings.TrimSpace(it.Evidence); e != "" {
		payload["evidence"] = e
	}
	if c := strings.TrimSpace(it.Confidence); c != "" {
		payload["confidence"] = c
	}
	if it.Confidence != "" && it.Confidence != "observed" && it.Confidence != "inferred" {
		return 0, fmt.Errorf("confidence must be observed/inferred")
	}
	intent := pid(it.IntentID)
	if intent <= 0 {
		intent = defaultIntent
	}
	if intent > 0 {
		node, err := t.ts.GetNode(intent)
		if err != nil || node == nil || node.Kind != db.KindIntent {
			return 0, fmt.Errorf("intent_id must be an intent of this task (intents of linked tasks are read-only)")
		}
	}
	// a fact is its OWN node kind (distinct from a vuln finding).
	var id int64
	var err error
	if it.Checkpoint != nil {
		id, err = t.ts.RecordCheckFact(ctx, payload, intent, *it.Checkpoint, t.worker, pidList(it.AssetIDs))
	} else {
		id, err = t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, pidList(it.AssetIDs))
	}
	if err != nil {
		return 0, err
	}
	if intent > 0 && it.Checkpoint == nil {
		_ = t.ts.Link(intent, db.RelYields, id) // chain: intent -> fact
	}
	t.writes.Facts++
	return id, nil
}

func (t *ToolSet) recordFact() actool.CoreTool {
	return t.writeExpTool("record_fact", "Write exploration [facts/conclusions] into the exploration graph, linked to the intent that produced them (intent_id). Used to record exploration results — both [positive conclusions] such as fingerprinting/enumeration and [negative conclusions] such as 'port closed'/'parameter not injectable'/'no login entry found'.\n"+
		"⚠️[Aggregate] multiple observations from one exploration [into a single fact]; do not split them. Whatever can be merged into one fact should be expressed as one fact: summary=a one-sentence summary of this conclusion, detail=the relevant details (may contain several specific items). Example: fingerprint intent → one fact {summary:'identified the tech stack and response characteristics of site X', detail:'nginx 1.25 / Vue3 / 200 / title=.. / body_len=..'}, rather than one fact each for status code, fingerprint and title. One intent usually produces just one fact; splitting too finely makes the graph grow without bound.\n"+
		"★The facts array is for writing several [mutually distinct] conclusions at once (each may omit intent_id, defaulting to the top-level intent_id). Returns an ids array of the same length and order as facts.\n"+
		"⚠️Only write conclusions you [actually saw] in tool output; do not imagine things. evidence and confidence keep inaccurate conclusions from polluting the graph:\n"+
		"  · evidence=[one line] of key evidence supporting this conclusion (command + the one or two most telling output lines), **keep it concise** — details are already in detail, do not paste large chunks of output here.\n"+
		"  · confidence=observed (seen directly in output) | inferred (deduced from symptoms).\n"+
		"  · **Negative conclusions** (not injectable/port closed/no entry found, etc.) should only state \"observation + tentative reading\" — say what you actually saw; whether to abandon the direction is for the planner to decide from the whole picture. Always give evidence; if the means were not exhausted or the evidence is weak (including probing only once, or it merely looks like it) mark inferred; mark observed only when truly exhausted and seen directly.",
		obj(map[string]any{
			"facts":      map[string]any{"type": "array", "description": "[Use when there are several distinct conclusions] Array of facts; elements have the same fields as the top-level fields below (summary/detail/evidence/confidence/intent_id/asset_ids); omitting intent_id uses the top-level intent_id. Returned ids have the same length and order as this array.", "items": map[string]any{"type": "object"}},
			"summary":    str("[one-sentence summary] of this exploration's conclusion (a digest of detail)"),
			"intent_id":  idp("id of the intent that produced this fact (the intent you picked up; default for each entry in a batch)"),
			"detail":     str("relevant details of this fact: put all the observed facts of this exploration here"),
			"evidence":   str("[one line] of key evidence: command + the one or two output lines that best prove the conclusion. Keep it concise; do not paste large chunks of output (details go in detail)."),
			"confidence": str("observed (seen directly in output) | inferred (deduced from symptoms). Label negative conclusions truthfully."),
			"checkpoint": checkpointSchema(),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "related asset ids (optional, 0/1/many): which assets this fact concerns"},
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Facts    []factItem `json:"facts"`
				factItem            // single mode + batch default intent_id
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid parameters"), nil
			}
			batch := len(a.Facts) > 0
			items := a.Facts
			if !batch {
				items = []factItem{a.factItem}
			}
			defaultIntent := pid(a.factItem.IntentID) // top-level intent_id = batch default

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.recordOneFact(ctx, it, defaultIntent)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // single: keep the original return
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("fact recorded: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type hintItem struct {
	Text        string            `json:"text"`
	AssetIDs    []json.RawMessage `json:"asset_ids"`
	TrafficRefs []db.TrafficRef   `json:"traffic_refs"`
}

// addOneHint attaches one hint node (active/human) to the exploration graph, optionally anchored to assets, and returns its id.
func (t *ToolSet) addOneHint(it hintItem) (int64, error) {
	if len(it.TrafficRefs) > 0 && !findingTrafficBindingEnabled() {
		return 0, fmt.Errorf("agent automatic traffic binding is disabled; hint carrying traffic_refs not saved. Enable it in system settings, or hand over text only")
	}
	if strings.TrimSpace(it.Text) == "" {
		return 0, fmt.Errorf("text must not be empty")
	}
	var anchors []int64
	for _, raw := range it.AssetIDs {
		if tid := pid(raw); tid > 0 {
			anchors = append(anchors, tid)
		}
	}
	refs, err := db.NormalizeTrafficRefs(it.TrafficRefs)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{"text": it.Text}
	if len(refs) > 0 {
		payload["traffic_refs"] = refs
	}
	// The planner is not woken here per entry — addHint triggers it once after the whole batch is written (with the hint text),
	// so one add_hint with several hints doesn't flood the planner's trigger lines entry by entry.
	return t.ts.AddNode(db.KindHint, payload, 0, "active", "human", anchors)
}

type goalItem struct {
	GoalType       string   `json:"goal_type"`
	RequiredChecks []string `json:"required_checks"`
	Text           string   `json:"text"`
	VulnClass      string   `json:"vulnclass"`
}

// addOneGoal attaches one goal node (open) to the exploration graph, linked to the task root (origin fact, rel spawns).
// origin comes from t.worker (default system): written as "goals" by the goals decomposer and as "human" by the main agent
// at runtime. Waking the planner is done by setGoals once after the whole batch (see below); this only persists.
func (t *ToolSet) addOneGoal(it goalItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text must not be empty")
	}
	payload := map[string]any{"text": text}
	if it.GoalType != "inspection" && it.GoalType != "outcome" {
		return 0, fmt.Errorf("a new goal must save goal_type=inspection/outcome; it cannot be reinterpreted at review time")
	}
	if it.GoalType == "inspection" && len(it.RequiredChecks) == 0 {
		return 0, fmt.Errorf("an inspection goal must save the required scope, mechanism and completion condition in required_checks")
	}
	payload["goal_type"] = it.GoalType
	if len(it.RequiredChecks) > 0 {
		for _, check := range it.RequiredChecks {
			if strings.TrimSpace(check) == "" {
				return 0, fmt.Errorf("required_checks must not contain empty items")
			}
		}
		payload["required_checks"] = it.RequiredChecks
	}
	if vc := strings.TrimSpace(it.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	origin := t.worker
	if origin == "" {
		origin = "system"
	}
	id, err := t.ts.AddNode(db.KindGoal, payload, 0, "open", origin, nil)
	if err != nil {
		return 0, err
	}
	if of, _ := t.ts.OriginFactID(); of > 0 && id > 0 {
		_ = t.ts.Link(of, db.RelSpawns, id) // goals descend from the task root (origin fact)
	}
	return id, nil
}

// setGoals adds exploration goals (goal nodes) to [this task]. It is both the goals decomposer's submit tool and the main
// agent's runtime tool for adding goals — the same managed tool, whose description/schema can be edited on the web and bound per agent.
func (t *ToolSet) setGoals() actool.CoreTool {
	return writeTool("set_goals",
		"Add exploration goals to [this task]. A goal = a final deliverable/verifiable result, not an attack step or recon action.\n"+
			"★Prefer batches: put several goals into the goals array and submit once; returns ids of the same length and order (failed items have id=0, details in errors). For a single one, omit goals and give a top-level text.\n"+
			"vulnclass is optional: the corresponding vulnerability class (e.g. SQLi/IDOR); leave empty for business-logic goals. Whether a goal is met is decided and marked by the system; this tool only adds goals.",
		obj(map[string]any{
			"goals":           map[string]any{"type": "array", "description": "[Prefer this] Array of goals to add, processed in order. Each element: text (required, one independently verifiable final goal) + vulnclass (optional). Returned ids have the same length and order as this array.", "items": map[string]any{"type": "object"}},
			"text":            str("[single] one independently verifiable final goal"),
			"vulnclass":       str("[single] corresponding vulnerability class (if clear), e.g. SQLi/IDOR; may be empty for business-logic goals"),
			"goal_type":       map[string]any{"type": "string", "enum": []string{"inspection", "outcome"}},
			"required_checks": map[string]any{"type": "array", "items": str("required objects/mechanisms/quantities and completion conditions")},
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_goals not enabled: ExplorationStore not initialized"), nil
			}
			var a struct {
				Goals    []goalItem `json:"goals"`
				goalItem            // single mode: top-level text/vulnclass
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Goals) > 0
			items := a.Goals
			if !batch {
				items = []goalItem{a.goalItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				if err := t.validateGoalRequest(ctx, it); err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				id, err := t.addOneGoal(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// Wake the planner (once per batch). Prefer notifyGoal: one set_goals records one "human added
				// N goals: …" trigger instead of one per goal; the decomposer/workers have no such callback → fall back to the bare notify (the decomposer's
				// round-0 doesn't even wire notify, i.e. a no-op, because the planner hasn't started yet).
				switch {
				case t.notifyGoal != nil:
					t.notifyGoal(addedTexts)
				case t.notify != nil:
					t.notify()
				}
				// Goals added by the main agent at runtime → pull a completed/paused task back to running (the terminal gate
				// swallows a plain notify, so it must be revived explicitly). Only mainagent wires this callback; decomposer/workers have nil.
				if t.resumeTask != nil {
					t.resumeTask()
				}
			}

			if !batch { // single: keep the original return
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("goal added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type constraintItem struct {
	Text string `json:"text"`
	Type string `json:"type"` // allow | deny
}

// addOneConstraint persists one operating constraint to task_constraints. origin comes from t.worker (default system):
// the decomposer writes "goals", the main agent writes "human".
func (t *ToolSet) addOneConstraint(it constraintItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text must not be empty")
	}
	kind := strings.TrimSpace(strings.ToLower(it.Type))
	if kind == "" {
		kind = "deny" // treat as deny by default: more conservative when the type is not given
	}
	if kind != "allow" && kind != "deny" {
		return 0, fmt.Errorf("type must be allow or deny")
	}
	return t.ts.AddConstraint(kind, text, t.worker)
}

// setConstraints adds operating constraints (allow = what may be done / deny = what is forbidden) to [this task]. It is both the
// submit tool for the goals decomposer's round-0 constraint extraction and the main agent's runtime tool for adding constraints — the same managed tool, whose
// description/schema can be edited on the web and bound per agent. Constraints are injected into the planner/worker system prompts to bound exploration.
func (t *ToolSet) setConstraints() actool.CoreTool {
	return writeTool("set_constraints",
		"Add operating constraints to [this task] to bound exploration: type=allow (permitted operations) or deny (forbidden operations).\n"+
			"A constraint = a rule about \"which operations may/may not be done\" (e.g. \"only test the current port, do not scan other ports\", \"no write operations on the production database\", \"passive recon only\"), not a goal and not an attack step.\n"+
			"★Prefer batches: put several into the constraints array and submit once; returns ids of the same length and order (failed items have id=0, details in errors). For a single one, omit constraints and give top-level text/type.\n"+
			"Only record constraints [explicitly stated] in the task goal/description; do not invent them. When unsure of the type, use deny (more conservative).",
		obj(map[string]any{
			"constraints": map[string]any{"type": "array", "description": "[Prefer this] Array of constraints to add, processed in order. Each element: text (required, one constraint) + type (allow|deny). Returned ids have the same length and order as this array.", "items": map[string]any{"type": "object"}},
			"text":        str("[single] the content of one operating constraint"),
			"type":        str("[single] allow or deny; defaults to deny"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_constraints not enabled: ExplorationStore not initialized"), nil
			}
			var a struct {
				Constraints    []constraintItem `json:"constraints"`
				constraintItem                  // single mode: top-level text/type
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Constraints) > 0
			items := a.Constraints
			if !batch {
				items = []constraintItem{a.constraintItem}
			}
			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.addOneConstraint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}
			if !batch { // single: keep the simple return
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("constraint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) addHint() actool.CoreTool {
	return t.writeExpTool("add_hint", "Attach a strategic hint from a human/the main agent to the exploration graph; the planner will read it the next time it generates intents.\n"+
		"★Prefer batches: put several hints into the hints array and submit once (saves round trips versus one call each). Returns an ids array of the same length and order as hints (failed items have id=0, details in errors). For a single one, omit hints and give a top-level text.",
		obj(map[string]any{
			"hints":        map[string]any{"type": "array", "description": "[Prefer this] Array of hints to add, processed in order. Each element has the same fields as the top-level fields below (text/asset_ids/traffic_refs). Returned ids have the same length and order as this array.", "items": obj(map[string]any{"text": str("hint content"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": HintTrafficSchema()})},
			"text":         str("[single] hint content, e.g. 'focus on post-authentication endpoints'"),
			"traffic_refs": HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "anchored asset ids (optional, 0/1/many)"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Hints    []hintItem `json:"hints"`
				hintItem            // single mode: top-level text/asset_ids
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Hints) > 0
			items := a.Hints
			if !batch {
				items = []hintItem{a.hintItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneHint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// Wake the planner (once per batch). Prefer notifyHint: one add_hint records one "human added
				// N strategic hints: …" trigger, so the planner knows "this round was fired by new hints" and sees their content;
				// without that callback fall back to the bare notify (bare wake; the hint is still folded into the graph for it to read).
				switch {
				case t.notifyHint != nil:
					t.notifyHint(addedTexts)
				case t.notify != nil:
					t.notify()
				}
			}

			if !batch { // single: keep the original return
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("hint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

// killWorkTool lets the planner terminate a single running work (by intent id).
func (t *ToolSet) killWorkTool() actool.CoreTool {
	return t.writeExpTool("kill_work", "Terminate a running intent (work). Used to stop exploration that has gone off track or is pointless; the terminated intent is marked stopped and is not picked up again automatically. Check what it is doing with get_worker_output first before deciding.",
		obj(map[string]any{"intent_id": idp("id of the intent to terminate (= work handle)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.killWork == nil {
				return actool.Errorf("kill_work is currently unavailable"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id is required"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id must be an intent of this task (intents of linked tasks are read-only)"), nil
			}
			if err := t.killWork(id); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("termination signal sent to the work of intent %d", id)), nil
		})
}

// steerWorkTool lets the planner inject a mid-run course-correction into a running
// work WITHOUT killing it: the message reaches the worker before its next tool call,
// which re-plans its next step (already-gathered context is kept). For in-intent
// nudges ("stop doing X, focus on Y"); if the whole direction is wrong use kill_work + a new intent.
func (t *ToolSet) steerWorkTool() actool.CoreTool {
	return t.writeExpTool("steer_work", "Inject a live course-correction instruction into a running intent (work) without interrupting it or losing existing progress: the worker receives your instruction before its next action and adjusts accordingly. Use it for [within-intent] corrections such as 'stop going down X, focus on Y'; if the whole direction is wrong, use kill_work and dispatch a new intent instead. It is advisable to check what it is doing with get_worker_output first.",
		obj(map[string]any{
			"intent_id": idp("id of the intent to steer (= work handle)"),
			"message":   str("course-correction instruction for the worker, stating clearly what to stop and what to switch to"),
		}, "intent_id", "message"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.steerWork == nil {
				return actool.Errorf("steer_work is currently unavailable"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Message  string          `json:"message"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id is required"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id must be an intent of this task (intents of linked tasks are read-only)"), nil
			}
			if strings.TrimSpace(a.Message) == "" {
				return actool.Errorf("message is required"), nil
			}
			if err := t.steerWork(id, a.Message); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("course-correction instruction injected into the work of intent %d (takes effect at the next step)", id)), nil
		})
}

// getWorkerOutput returns a work's final (or as-of-abort) conclusion text by intent id.
func (t *ToolSet) getWorkerOutput() actool.CoreTool {
	return t.readExpTool("get_worker_output", "Get the final output conclusion of an intent (work) of this task or a directly linked task. Results from linked tasks carry source_task_id/inherited=true and are read-only. A normally finished work returns its summary; a terminated (stopped)/failed work returns its last output up to the abort.",
		obj(map[string]any{"intent_id": idp("intent id (= work handle)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id is required"), nil
			}
			intentNode, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id does not belong to this task or its directly linked tasks"), nil
			}
			acts, _, err := t.ts.ActivityListWithSources(id, 0, 1000)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var chosen, fallback *db.Activity
			for i := range acts {
				switch acts[i].Kind {
				case "result":
					chosen = &acts[i]
					fallback = &acts[i]
				case "text":
					fallback = &acts[i]
				}
			}
			pick := chosen
			if pick == nil {
				pick = fallback
			}
			if pick == nil {
				if intentNode.Inherited {
					return jsonResult(inheritedMap(map[string]any{
						"intent_id": id, "final_text": "(this work has no output yet)",
					}, intentNode.SourceTaskID))
				}
				return actool.Text("(this work has no output yet)"), nil
			}
			detail, _ := t.ts.ActivityDetailWithSources(pick.ID)
			if detail == "" {
				detail = pick.Summary
			}
			result := map[string]any{
				"intent_id": id, "final_text": detail,
				"summary": pick.Summary, "is_error": pick.IsError,
			}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// traceSteps renders summary-only trace rows, re-truncating each summary to 100
// chars — the stored summary is capped at 200 for the UI transcript; the trace
// tools want it tighter since a whole work's step list is many rows.
func traceSteps(acts []db.Activity) []map[string]any {
	steps := make([]map[string]any, 0, len(acts))
	for i := range acts {
		step := map[string]any{
			"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
			"is_error": acts[i].IsError, "summary": firstLine(acts[i].Summary, 100),
		}
		if acts[i].Inherited {
			inheritedMap(step, acts[i].SourceTaskID)
		}
		steps = append(steps, step)
	}
	return steps
}

// getWorkerTrace exposes a work's execution PROCESS (not just its final output):
// list step summaries, keyword-search within one work, or pull full detail of a
// few specific steps. Thinking steps are excluded everywhere.
func (t *ToolSet) getWorkerTrace() actool.CoreTool {
	return t.readExpTool("get_worker_trace",
		"View the [execution process] of an intent (work) (unlike get_worker_output, which only gives the final conclusion). Three usages:\n"+
			"① intent_id only → returns the stream of per-step summaries of the work (summary ≤100 chars, with step_id; only an outline of actions, no full output);\n"+
			"② intent_id + q → returns only the summaries of steps matching the keyword (searched in both summaries and full output; still summaries only — use ③ to see content);\n"+
			"③ intent_id + step_ids → returns the full content (detail) of those steps; at most 5 per call — beyond that only the first 5 are returned and the rest are reported in notice/omitted_step_ids.\n"+
			"Typical flow: first locate suspicious steps' step_id with ①/②, then fetch their full output with ③. Thinking steps are not included. Historical traces of directly linked tasks are supported; their results carry source_task_id/inherited=true and are read-only.",
		obj(map[string]any{
			"intent_id": idp("intent id (= work handle)"),
			"q":         str("keyword: return only steps whose summary/full output matches it (optional; mutually exclusive with step_ids)"),
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "step_ids whose full content to fetch (from ①/② results; at most 5 per call — extra ones are not returned and are listed in omitted_step_ids)"},
			"limit":     intp("return limit for the summary stream/search (optional)"),
		}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage   `json:"intent_id"`
				Q        string            `json:"q"`
				StepIDs  []json.RawMessage `json:"step_ids"`
				Limit    int               `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id is required"), nil
			}
			intentNode, nodeErr := t.ts.GetNodeWithSources(id)
			if nodeErr != nil {
				return actool.Errorf(nodeErr.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id does not belong to this task or its directly linked tasks"), nil
			}
			// ③ detail drill-down by step ids, thinking excluded by the store.
			if len(a.StepIDs) > 0 {
				// Dedup + drop invalid ids first so garbage/duplicates don't eat into
				// the per-call cap. detail is returned in full (untruncated), so the
				// cap bounds one tool result; over the cap we serve the first N and
				// tell the model exactly which ids were deferred, instead of erroring
				// and forcing it to re-plan the call.
				const maxStepIDs = 5
				var ids []int64
				seen := make(map[int64]bool)
				for _, raw := range a.StepIDs {
					if v := pid(raw); v > 0 && !seen[v] {
						seen[v] = true
						ids = append(ids, v)
					}
				}
				var omitted []int64
				if len(ids) > maxStepIDs {
					omitted = append(omitted, ids[maxStepIDs:]...)
					ids = ids[:maxStepIDs]
				}
				acts, err := t.ts.ActivityByIDsWithSources(ids)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				steps := make([]map[string]any, 0, len(acts))
				for i := range acts {
					if acts[i].NodeID == nil || *acts[i].NodeID != id || acts[i].Inherited != intentNode.Inherited ||
						(acts[i].Inherited && acts[i].SourceTaskID != intentNode.SourceTaskID) {
						continue
					}
					step := map[string]any{
						"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
						"is_error": acts[i].IsError, "detail": acts[i].Detail,
					}
					if acts[i].Inherited {
						inheritedMap(step, acts[i].SourceTaskID)
					}
					steps = append(steps, step)
				}
				result := map[string]any{"intent_id": id, "steps": steps, "returned_step_ids": ids}
				if len(omitted) > 0 {
					// returned_step_ids/omitted_step_ids let the model decide programmatically
					// whether another call is worth it; the notice states the same in prose.
					result["omitted_step_ids"] = omitted
					result["notice"] = fmt.Sprintf(
						"At most %d steps' full content per call; this call returned the first %d (%v); the %d not fetched are %v. "+
							"If this content is enough to pinpoint the issue, there is no need to fetch the remaining steps; if you really need to continue, call again with those step_ids.",
						maxStepIDs, len(ids), ids, len(omitted), omitted)
				}
				if intentNode.Inherited {
					inheritedMap(result, intentNode.SourceTaskID)
				}
				return jsonResult(result)
			}
			// ①/② summary stream, optionally keyword-filtered; 100-char summaries.
			var acts []db.Activity
			var err error
			if strings.TrimSpace(a.Q) != "" {
				acts, err = t.ts.ActivityTraceSearchWithSources(id, a.Q, a.Limit)
			} else {
				acts, err = t.ts.ActivityTraceWithSources(id, a.Limit)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			result := map[string]any{"intent_id": id, "steps": traceSteps(acts)}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// searchAllWorkerTraces keyword-searches EVERY work's process in this task — for
// finding what a worker saw but never wrote back as a fact. Returns only matching
// summaries (≤100 chars), each tagged with its intent_id for follow-up drill-down.
func (t *ToolSet) searchAllWorkerTraces() actool.CoreTool {
	return t.readExpTool("search_all_worker_traces",
		"[Usually not recommended, since the system already provides most information] Search [the execution of other works in this task] by keyword (q) — used to recover something a worker saw but didn't write into a fact (a path/token/error, etc.). "+
			"Steps of your own intent are excluded automatically (they are already in your context). "+
			"Returns only summaries of matching steps (summary ≤100 chars), each with intent_id; then fetch full content with get_worker_trace(intent_id, step_ids=[...]).",
		obj(map[string]any{
			"q":     str("keyword (searched in the summaries + full output of all work steps)"),
			"limit": intp("return limit, default 100 (optional)"),
		}, "q"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Q) == "" {
				return actool.Errorf("q is required"), nil
			}
			// Exclude the caller's own intent steps (a worker's own trace is already in its context).
			acts, err := t.ts.ActivityTraceSearchAllWithSources(t.ownerNode, a.Q, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			hits := make([]map[string]any, 0, len(acts))
			for i := range acts {
				var intent int64
				if acts[i].NodeID != nil {
					intent = *acts[i].NodeID
				}
				hit := map[string]any{
					"intent_id": intent, "step_id": acts[i].ID, "worker": acts[i].Worker,
					"kind": acts[i].Kind, "tool": acts[i].Tool, "is_error": acts[i].IsError,
					"summary": firstLine(acts[i].Summary, 100),
				}
				if acts[i].Inherited {
					inheritedMap(hit, acts[i].SourceTaskID)
				}
				hits = append(hits, hit)
			}
			return jsonResult(map[string]any{"query": a.Q, "hits": hits})
		})
}

// listWorkerTraces gives a worker (which has no graph_overview and can't see the
// intent graph) a lightweight index of the works in this task — intent_id +
// one-line summary + state — so it can DISCOVER which works to inspect via
// get_worker_trace. Without this a worker only knows intent_ids that come back
// from search_all_worker_traces hits. Excludes still-open intents (not yet run →
// no process to inspect).
func (t *ToolSet) listWorkerTraces() actool.CoreTool {
	return t.readExpTool("list_worker_traces",
		"[Usually not recommended, since the system already provides most information] List an index of [works (intents) already run] in this task: intent_id + one-sentence direction (summary) + state. "+
			"You (the worker) cannot see the exploration graph; use this to discover which works are worth looking into — then view their steps with get_worker_trace(intent_id) and details with get_worker_trace(intent_id, step_ids=[...]). "+
			"Lists only executed ones (running/done/exhausted/blocked/stopped), not open ones that haven't run yet. Note: your task boundary is still the intent you picked up; looking at other works is only for reusing observations/avoiding duplicate effort.",
		obj(map[string]any{
			"q":     str("filter by summary keyword (optional)"),
			"limit": intp("return limit, default 50 (optional)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = 50
			}
			all, err := t.ts.ListByKindWithSources(db.KindIntent, 500)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Q))
			out := make([]map[string]any, 0, limit)
			for _, n := range all {
				if n.Inherited && n.State == "running" {
					continue
				}
				switch n.State {
				case "running", "done", "exhausted", "blocked", "stopped": // has run → has a process
				default:
					continue
				}
				var p map[string]any
				_ = json.Unmarshal(n.Payload, &p)
				summary, _ := p["summary"].(string)
				if q != "" && !strings.Contains(strings.ToLower(summary), q) {
					continue
				}
				item := map[string]any{"intent_id": n.ID, "summary": summary, "state": n.State}
				if n.Inherited {
					inheritedMap(item, n.SourceTaskID)
				}
				out = append(out, item)
				if len(out) >= limit {
					break
				}
			}
			return jsonResult(map[string]any{"works": out})
		})
}

// PlannerTools is the read + intent-generation + goal-judgement tool set.
func (t *ToolSet) PlannerTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		// cold-digest §6.1: restore folded cold nodes (digest body → members → detail).
		t.expandDigest(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.listGoals(), t.listRemainingWork(), t.addIntent(), t.proveGoal(), t.reopenGoal(), t.goalMet(),
		t.killWorkTool(), t.steerWorkTool(),
		// report_finding: if the planner itself has confirmed a finding while assessing the situation, it can record it directly (same tool as the worker).
		t.addFinding(),
		// list_companies: view the company list + scope + asset counts (to get company_id / understand ownership scope).
		t.listCompanies(),
		// list_assets: when planning, search the whole asset store by DSL (complements list_untested_assets' "untested within scope" view
		// with the ability to "query the whole store by domain/fingerprint/port/status code, etc.").
		t.listAssets(),
		// add_company_scope: when planning, domains/IPs/CIDRs/ICP/keywords can be added to a company's asset scope (automatically claiming matching assets).
		t.addCompanyScope(),
		// add_task_scope: proactively bring a whole root domain/whole company/a subdomain/IP into this task's test scope (coverage denominator).
		t.addTaskScope(),
		// list_untested_assets: query untested assets within this task's scope on demand (type + paging) and decide yourself whether to test them.
		t.listUntestedAssets(),
	}
}
