// ARTEX domain model — types used across the UI.
// Derived from the functional spec (section 7: key data shapes).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface TaskCompletion {
  attempt_id: number;
  phase: "idle" | "draining" | "reviewing" | "completed" | "interrupted";
  reason: string;
  started_at: string;
  deadline_at: string;
  updated_at: string;
  review?: string;
  goal_gaps?: { goal_id: number; expected_version: number; reason: string }[];
  remaining_work?: {
    intent_id: number;
    state: string;
    summary: string;
    kind?: string;
    reason?: string;
    reference_id?: number;
  }[];
  review_deadline_at?: string;
  failure_kind?: string;
  failure_count?: number;
  next_retry_at?: string;
  recovery_action?: string;
  recovery_history?: { at: string; actor: string; reason: string; failure_kind: string; failure_count: number }[];
}

export interface Task {
  distinct_findings?: FindingDistinctStats;
  id: string;
  name?: string; // optional task name; empty/omitted = unnamed, display falls back to the description
  category_id?: number;
  category_name?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  completion?: TaskCompletion;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  findings?: { critical: number; high: number; medium: number; low: number }; // recorded findings (by severity)
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // asset coverage switch (set at creation, on by default); false = coverage not computed/shown
}

export interface TaskCategory {
  id: number;
  name: string;
  task_count: number;
  created_at: string;
  updated_at: string;
}

export interface TaskTemplate {
  id: number;
  name: string;
  description: string;
  goal: string;
  category_id?: number | null; // preset category; null/omitted = none
  intercept_rules?: AssetInterceptRuleInput[]; // preset task-level block/allow rules
  created_at: string;
  updated_at: string;
}

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats {
  calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface TaskArchive {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
  created_at: string;
  updated_at: string;
}

export interface TaskArchivePage {
  items: TaskArchive[];
  total: number;
  page: number;
  size: number;
}

export interface ArchiveBatchItem {
  id: string;
  archive_id?: number;
  ok: boolean;
  queued: boolean;
  error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // owning company asset id; empty = unowned
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// A node of the force-directed "asset coverage graph". Unique key: asset="a:<id>", company="c:<id>",
// root domain without an asset row="r:<domain>". in_scope=false marks gray context nodes used only for links.
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// Intents/facts/findings linked to an asset in this task's exploration graph (for the coverage graph node drawer).
export interface CoverageAssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id?: string;
  inherited?: boolean;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  size: number;
  mtime: number; // unix millis
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// One task test scope entry (coverage denominator + authorization boundary).
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // resolved by a backend JOIN on companies; set only for kind=company
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// Structured asset scope rules submitted when adding a company.
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// Result of writing asset scope. errors are invalid lines in this submission; warnings are existing data problems
// unrelated to this submission that would skew ownership (e.g. assets with a hostname in the ip field).
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// One company asset scope rule (the single source of truth for ownership).
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; // set when kind=domain
  net?: string; // set when kind=ip|cidr
  value?: string; // may be returned directly by the backend when kind=icp|keyword
  raw: string; // raw user input, for display and refill
  reason?: string;
}

// Company: a type=company asset node + icon + asset count + asset scope rules.
export interface Company {
  id: number;
  name: string;
  logo?: string; // remote icon URL; if empty the front end uses the name's initial
  asset_count: number;
  scope?: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

export interface TaskNode {
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  source_task_id?: string;
  inherited?: boolean;
  delete_reason?: string; // delete reason when the intent was soft-deleted (state='deleted')
}

// One feed board page: nodes paginated in creation order + the edges on this page + the nodes at the other end (refs, indexed by id),
// so every entry can tell "where it came from and what it produced" without loading the whole graph.
export interface ExplorationNodePage {
  items: TaskNode[];
  total: number;
  page: number;
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // node id → assets anchored by that node (shown when a feed entry expands; includes this page's nodes and their neighbors).
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// Goal for the goal management card (the backend already split the payload into text/vulnclass).
export interface TaskGoal {
  goal_type?: "inspection" | "outcome";
  required_checks?: string[];
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
  content_version?: number;
  audit?: {
    at: string;
    event: "prove" | "reject" | "reopen" | "edit";
    actor: string;
    reason: string;
    previous_state: string;
    evidence_id?: number;
  }[];
}

// Action constraint for the constraint management card (allow / deny).
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// Finding status: pending / in progress / confirmed / resolved / fixed / false positive / ignored / duplicate / risk accepted.
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted";

// FindingAsset is an asset bound to a finding (label pre-rendered by the backend).
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

export interface Finding {
  evidence_assessment?: "needs_evidence" | "needs_review" | "supported_for_goal" | "unsupported_for_goal";
  case_id?: string;
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // row id in the standalone findings table, the handle for status updates (may be missing on old task nodes)
  vulnclass: string;
  name?: string; // finding name; display falls back to vulnclass when empty
  severity: Severity;
  status: FindingStatus;
  summary: string;
  evidence: string;
  report?: string; // detailed report (Markdown); only returned by the detail endpoint, empty in lists
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  source_task_id?: string;
  inherited?: boolean;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage is the server-paginated response of the findings list.
export interface FindingsPage {
  items: Finding[];
  total: number;
  page: number;
  page_size: number;
}

export interface FindingGroup {
  task_id: string | number | null;
  task_name?: string; // optional task name; empty/omitted = unnamed
  task_description: string;
  task_status: string;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingGroupsPage {
  items: FindingGroup[];
  total: number;
  finding_total: number;
  page: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats is the whole-table findings aggregate (stat cards + vulnerability class dropdown), computed server-side and unaffected by pagination.
export interface FindingStats {
  distinct?: FindingDistinctStats;
  total: number;
  pending: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption is one entry of the findings page "by task" filter: a task with findings (an empty description means the task was deleted,
// and the front end falls back to showing the id) and its finding count.
export interface FindingTaskOption {
  id: string | number;
  name?: string; // optional task name; empty/omitted = unnamed
  description: string;
  count: number;
}

// FindingQuery holds the pagination/filter/sort parameters of the findings list.
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // task id; "all"/empty = no task filter
  query?: string;
  sort?: "severity" | "time";
  // asset tree node key; selecting a node = selecting its whole subtree. Empty = no asset filter.
  assetScope?: string;
}

// ---- Findings by asset (asset view) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode is a node of the asset tree. key looks like a:<id> (asset), c:<id> (company),
// r:<domain> (root domain without an asset row), __none__ (no linked asset).
export interface FindingAssetNode {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // findings attached directly to this asset
  total: number; // including descendants, deduplicated by finding
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET matches the backend's db.FindingUnassignedAsset.
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment is an uploaded file: path is relative to the conversation/task workdir (the agent's CWD).
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // absolute path (returned for scope=staging uploads; written into the description before the task is created)
}

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
  };
  source_task_id?: string;
  inherited?: boolean;
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile {
  id: number;
  name: string;
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- Agent triggers (P3 scheduling, custom agents only) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // schedule: every N seconds (0 = no schedule)
  on_finding: boolean; // fires when any task records a finding
  on_goal_met: boolean; // fires when any task meets a goal
  on_task_timeout: boolean; // fires when any task times out
  on_tool_call: boolean; // fires when a selected tool is called (on completion)
  on_task_create: boolean; // fires when any task is created
  interval_message: string; // separate user message per trigger condition
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // tool keys selected for on_tool_call (at least one)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system";
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface SessionTokenUsage {
  session: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface BatchControlItem {
  id: string;
  ok: boolean;
  status?: string;
  queued?: boolean;
  error?: string;
}

// Per-task result of a bulk category change. Failure only means the task was deleted; the category write itself is atomic.
export interface BatchCategoryItem {
  id: string;
  ok: boolean;
  error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
  profile_name: string;
  calls: number;
  tasks: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
  profile_name: string;
  date: string; // YYYY-MM-DD
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  source_task_id?: string;
  inherited?: boolean;
  seg?: number; // main-agent session: which conversation segment (0 = original)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  completion_review_seconds?: number;
  completion_review_turns?: number;
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // agent auto-binds traffic evidence, off by default; manual binding is unaffected
  llm_record: boolean; // LLM recording switch (off by default); when off no LLM calls are recorded
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // Separate egress proxy (http/https/socks5) for the search endpoint; unrelated to the traffic-recording MITM proxy. Empty = direct.
  web_search_proxy?: string;
  // Global egress proxy (http/https/socks5, may include user:pass) for all target traffic. With traffic capture on it is the
  // MITM upstream; with capture off it is injected directly into the agent's bash/WebFetch. Empty = direct.
  global_proxy?: string;
  python_interpreter?: string; // python interpreter path for custom script tools (empty = detected at runtime)
  workers?: number; // number of concurrent worker agents (default 3); applies to tasks started afterwards
  // Task concurrency limit: max tasks "running" at once. Off = unlimited; when on, new tasks over the limit queue and start automatically when a slot frees up.
  task_concurrency_enabled?: boolean; // default false
  task_concurrency_limit?: number; // default 5 when enabled
  // LLM rotation (failover). Off by default; when on, agents "without a pinned model" switch to the next profile
  // automatically when the current one is unavailable (insufficient balance/invalid key/rate limit/service error).
  llm_pool_enabled?: boolean; // default false
  // Whether agents/tasks bound to a specific profile also fall back to the rotation chain on failure. Default false = binding is exclusive.
  llm_pool_bind_fallback?: boolean;
  // Action constraint injection scope (all on by default): appends the task's allow/deny constraints to the matching agents' system prompts.
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // Experimental: noa model-driven context compaction (off by default). When on, the four platform agent kinds (planner/
  // worker/main agent/chat) hand context compaction to noa instead of the built-in compaction; read once per run, applies to
  // runs started afterwards.
  noa_compaction?: boolean;
  // ---- Finding IM notifications (channels are separate resources, see /api/notify/*; only three global settings here) ----
  notify_enabled?: boolean; // master notification switch, on by default; a one-click kill switch during maintenance
  notify_public_base_url?: string; // external base URL for finding detail links; empty = messages carry no link
  notify_digest_interval_min?: number; // digest mode interval (minutes), default 30
}

// ---- Finding IM notifications ----

// NotificationFilter holds a channel's filters; every field is optional and omitted means no filtering.
// The backend validates none of the fields: a malformed config counts as a "match" (better to over-notify than miss one).
export interface NotificationFilter {
  min_severity?: string; // "" | low | medium | high | critical
  task_ids?: number[]; // empty = any; otherwise must intersect the finding's tasks
  asset_ids?: number[]; // empty = any; otherwise must intersect the finding's anchored assets
  vulnclass_include?: string[]; // empty = accept all; otherwise the vulnerability class must match a keyword (case-insensitive substring)
  vulnclass_exclude?: string[]; // matching any keyword excludes (exclude wins over include)
  on_status_change?: boolean; // whether to also receive finding status change events
}

// NotificationChannel is a channel instance. config fields vary by kind,
// and credential fields are replaced on read with masked values starting with "__masked__"; sending them back unchanged means "unchanged".
export interface NotificationChannel {
  id: number;
  name: string;
  kind: string;
  enabled: boolean;
  mode: "realtime" | "digest";
  config: Record<string, unknown>;
  filter: NotificationFilter;
  rate_per_min: number;
  created_at: string;
  updated_at: string;
  // secret_keys come from the backend per channel type; the front end renders password fields and the "leave empty to keep" hint from them,
  // without hardcoding any channel knowledge.
  secret_keys: string[];
}

// NotificationKind is the channel type metadata returned by /api/notify/meta.
export interface NotificationKind {
  kind: string;
  default_rate_per_min: number;
  secret_keys: string[];
}

export interface NotificationMeta {
  kinds: NotificationKind[];
  enabled: boolean;
  public_base_url: string;
  digest_interval_min: string;
  defaults: { digest_interval_min: number };
  stats: {
    channels: number;
    channels_on: number;
    pending: number;
    failed: number;
    sent_today: number;
    backlog_age_ms: number;
  };
}

// NotificationDelivery is a delivery record, used for delivery history and resending failures.
export interface NotificationDelivery {
  id: number;
  finding_id: string;
  event_kind: string; // finding_created | finding_status_changed
  channel_id: number;
  channel_name: string;
  channel_kind: string;
  state: "pending" | "sending" | "sent" | "failed" | "skipped";
  attempts: number;
  last_error: string;
  batch_id?: number;
  created_at: string;
  sent_at?: string;
  next_attempt_at: string;
  title: string;
  severity: string;
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // thinking switch (thinking.type): "" = not sent (default) | "disabled" = off | "enabled" = on
  thinking_type?: string;
  // thinking effort: "" = not sent (default) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // rotation order: higher is picked first. The active profile is always first in the chain regardless of this value.
  priority?: number;
  // true = not a failover target (can still be bound explicitly by agents/tasks).
  pool_exclude?: boolean;
  // true (default) = streaming (SSE) | false = true non-streaming (stream:false, returned at once).
  streaming?: boolean;
  // output cap per reply (tokens). 0 = field not sent, the server default applies.
  // Not to be confused with context_window_k: that is the model's total capacity, used locally only for the compaction threshold.
  max_tokens?: number;
  // Which request field name carries the cap; only meaningful for format="openai":
  // "" = max_tokens (default) | "max_completion_tokens" (the only one OpenAI reasoning models accept)
  max_tokens_field?: string;
  // Custom session header name: when non-empty every request sends this HTTP header with the current conversation/intent session id as value.
  // "" = not sent. For gateways that do prompt caching/sticky routing by a session-id header.
  session_header_key?: string;
  // Retry overrides of this profile (connect/empty response/same-provider safe window). Empty/all 0 = follow the global policy.
  retry?: LLMRetryOverride;
}

// ---- LLM retry policy ----
// The two knobs of one retry layer. For both, "0 = not configured":
//   attempts    0 = default count | -1 = disable this layer | >0 = retry count
//   interval_ms 0 = default exponential backoff | >0 = use this fixed interval in milliseconds
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// The three layers a single LLM profile can override (all endpoint-bound retries).
export interface LLMRetryOverride {
  connect: LLMRetryRule; // connect retry: connection reset/timeout/429/5xx before the stream starts
  empty: LLMRetryRule; // empty response retry: completed with no content (openai format only)
  stream: LLMRetryRule; // same-provider safe window retry: replay a dropped stream before any output was delivered
}

// Global policy = defaults for the three layers above + two global-only layers:
//   breaker rotation circuit breaker (attempts = consecutive transient failures before tripping, interval_ms = fixed cooldown)
//   intent  intent rerun (rerun the whole intent after a worker ends with model_error)
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- LLM rotation (failover) ----
// A profile's position and health in the rotation chain. state:
//   ok       healthy
//   degraded consecutive failures below the trip threshold
//   tripped  tripped, skipped during cooldown (cooldown_secs is the remaining seconds)
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  priority: number;
  active: boolean; // whether this is the active profile (always first in the chain)
  excluded: boolean; // pool_exclude: not part of the rotation
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; // built-in: goals/planner/mainagent/worker; custom: a user-defined key
  name: string;
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // bound LLM profile; null/absent = follow task/conversation/global
  max_turns?: number; // 0 = unlimited
  run_seconds?: number; // wall-clock limit per worker run (seconds); 0 = unlimited
  web_search?: boolean; // whether web search is enabled (gated by the global system switch)
  interactive_shell?: boolean; // whether the interactive shell is enabled (persistent PTY session tool family)
  // P3 post-trigger handling policy (only meaningful for custom agents)
  trigger_run_mode?: "serial" | "parallel"; // serial queue / one concurrent conversation per trigger
  trigger_merge_mode?: "by_task" | "all" | "none"; // serial only: merge by task / merge all / no merging
  trigger_max_parallel?: number; // parallel only: concurrency limit per agent; 0 = unlimited
  // binding counts (list endpoint only): visible MCP / visible skills / bound tools
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  // bindable LLM profile candidates (for the "default model" dropdown); the current binding is agent.llm_profile_id
  llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
  wrapup_prompt?: string; // saved wrap-up prompt (empty = built-in default)
  wrapup_default?: string; // built-in default wrap-up prompt (placeholder/restore default)
  wrapup_max_turns?: number; // saved wrap-up rounds (0 = built-in default)
  wrapup_max_turns_default?: number; // built-in default wrap-up rounds (for the "0 = default N" hint)
  // task-level timeout wrap-up text (worker/planner only; the section is shown only when task_timeout_wrapup_supported=true)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // Call stats (skill_usage ledger). Skills never called: calls=0, last_used omitted.
  calls: number;
  tasks: number; // number of tasks that loaded it (chat conversations not counted)
  usage_agents: string[]; // agent keys that loaded it
  last_used?: string;
}

// SkillCall is one Skill() call (recent calls of a single skill).
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = not in a task (chat conversation)
  session_id: string;
  args_len: number;
}

// MissingSkill is a skill that was requested but does not exist: a "wanted but missing" gap.
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

// ---- Tools (built-in tool catalog) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // custom tool kind
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // custom tool execution spec (kind!=builtin)
  deferred?: boolean; // deferred schema (SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  created_at: string;
  updated_at: string;
}

// ---- Asset Intercept Rules (asset intercept: global blocklist) ----
export type AssetInterceptKind =
  | "exact_domain"
  | "exact_ip"
  | "exact_url"
  | "fuzzy_domain"
  | "fuzzy_ip"
  | "fuzzy_url"
  | "cidr";

// action is only for task-level rules: block = block (testing forbidden), allow = allow (allowlist).
export type AssetInterceptAction = "block" | "allow";

// Input entry for task-level asset block/allow rules (used when creating a task and editing task details).
export interface AssetInterceptRuleInput {
  action: AssetInterceptAction;
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  enabled: boolean;
}

export interface AssetInterceptRule {
  id: number;
  enabled: boolean;
  action?: AssetInterceptAction; // global rules omit this field (always block); task-level rules distinguish block/allow
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  reason: string; // rule message or model verdict reason (model verdicts carry the [model] prefix)
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: global config for model fallback approval (the model decides only when no intercept rule matched).
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = follow the active/default profile
  prompt: string; // judge prompt; when unset, GET returns the full built-in template
  timeout_seconds: number; // model call timeout
  fail_action: "allow" | "ask" | "deny"; // fallback when the model errors/times out/cannot be parsed
  ask_timeout_seconds: number; // approval wait timeout after the model escalates with ask
  ask_timeout_action: "allow" | "deny"; // default action after the approval times out
}

// JudgeUsage: cumulative token usage of model fallback approval (judge channel) + a daily series for the last N days.
export interface JudgeDayUsage {
  date: string; // YYYY-MM-DD (UTC)
  calls: number;
  input_tokens: number;
  output_tokens: number;
}
export interface JudgeUsage {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  daily: JudgeDayUsage[];
}

export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "unknown";
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
}

// ── Asset sync (ScopeSentry data source) ──────────────────────────────────────────────
export interface SSProject {
  id: string; // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string; // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

export type SSSyncStatus = "running" | "success" | "partial" | "failed" | "interrupted";

export interface SSSyncRequest {
  background?: boolean;
  retry_run_id?: number;
  dimension: "project" | "task";
  targets: string[];
  target_ids?: Record<string, string>;
  asset_types: string[];
  create_company?: boolean;
  page_size?: number;
}

export interface SSSyncStats {
  fetched: number;
  written: number;
  skipped: number;
  failed: number;
  derived_ip_written: number;
  derived_ip_failed: number;
  created?: number;
  updated?: number;
  unchanged?: number;
}

export interface SSSyncTarget {
  target: string;
  source_target_id?: string;
  asset_type: string;
  status: SSSyncStatus;
  stats: SSSyncStats;
  truncated: boolean;
  error?: string;
}

export interface SSSyncResult {
  run_id?: number;
  status?: SSSyncStatus;
  synced: Record<string, number>;
  companies: string[] | null;
  warnings: string[] | null;
  errors: string[] | null;
  stats?: SSSyncStats;
  counting?: string;
  targets?: SSSyncTarget[];
  started_at?: string;
  finished_at?: string;
  unique_assets?: {
    total: number;
    created: number;
    updated: number;
    unchanged: number;
    direct: number;
    derived: number;
  };
}

export interface SSSyncRun {
  id: number;
  status: SSSyncStatus;
  started_at: string;
  finished_at?: string;
  request: SSSyncRequest;
  result: SSSyncResult | null;
}

export interface SSSyncItem {
  raw?: Record<string, unknown> | null;
  raw_omitted?: boolean;
  id: number;
  target: string;
  source_target_id?: string;
  asset_type: string;
  source_asset_id?: string;
  status: string;
  reason?: string;
  asset_id?: number;
  derived_ip_written: number;
  derived_ip_failed: number;
  outcome?: "created" | "updated" | "unchanged";
  asset_changes?: { asset_id: number; type: string; outcome: "created" | "updated" | "unchanged"; direct: boolean }[];
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// Call stats of a single tool (/commands/stats); errors is the number of failed calls.
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  // Raw HTTP exchanged with the provider: the request is the full body sent by buildBody() (including tool
  // schemas), the response is the raw SSE frames. request_body/response_body above are the normalized view,
  // which drops tool schemas and tool_use blocks. Empty for old records.
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  url: string;
  method: string;
  status: number;
  content_type: string;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
  resp_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

/** GET /api/update/check: comparison of the current version with the latest GitHub release. */
export interface UpdateCheck {
  /** Currently running version; dev builds report "dev" or a suffixed git describe form. */
  current: string;
  /** Runtime form. Under docker an update only touches the container's writable layer; recreating the container reverts to the image version. */
  mode: "docker" | "binary";
  os: string;
  arch: string;
  repo: string;
  /** Whether a previous version (artex.old) is available for rollback. */
  has_backup: boolean;
  /** Outcome of the self-update bootstrap at this startup (swap failed / rolled back, etc.); empty if nothing happened. */
  boot_notice?: string;
  rolled_back?: boolean;
  /** Reason when the GitHub query failed; the fields below are absent in that case. */
  error?: string;
  latest?: string;
  notes?: string;
  html_url?: string;
  published_at?: string;
  /** Release asset name for the current platform, and whether the release actually includes it. */
  asset?: string;
  asset_available?: boolean;
  size?: number;
  has_update?: boolean;
  /** Whether the two versions are comparable; false for dev builds, which disables one-click update. */
  comparable?: boolean;
  /** Explanation when comparable is false. */
  reason?: string;
}

/** One update progress event pushed by /api/update/stream. */
export interface UpdateProgress {
  phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
  /** Only meaningful during download (0-100); -1 in other phases. */
  percent: number;
  message: string;
  version?: string;
  error?: string;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}

export interface FindingDistinctStats {
  total: number;
  reports: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  unassessed: number;
}
export interface FindingCase {
  id: string;
  task_id: string | null;
  title: string;
  reason: string;
  severity: Severity | "";
  severity_reason: string;
  report?: string;
  version: number;
  report_version: number;
  active: boolean;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  created_at: string;
}
export interface FindingCaseListRow {
  case?: FindingCase | null;
  finding?: Finding;
  matched_ids: number[];
}
export interface FindingCasePage {
  items: FindingCaseListRow[];
  total: number;
  page: number;
  page_size: number;
  stats: FindingDistinctStats;
}
export interface FindingCaseEvent {
  id: string;
  action: string;
  actor: string;
  reason: string;
  finding_ids: number[];
  created_at: string;
}
export interface FindingCaseDetail {
  case: FindingCase;
  events: FindingCaseEvent[];
}
export interface FindingCaseSuggestion {
  id: string;
  task_id: string;
  left_id: string;
  right_id: string;
  title: string;
  reason: string;
  state: string;
}
export interface FindingCaseReviewRun {
  conversation_id: number;
  task_id: string;
  state: "queued" | "running" | "done" | "failed";
  error: string;
}
