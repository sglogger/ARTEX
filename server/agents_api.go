package server

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// reAgentKey mirrors the agents.key DB check: lowercase letter start, then
// lowercase letters / digits / underscores.
var reAgentKey = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (s *Server) pgListAgents(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	ags, err := pg.ListAgents()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	dtos := agentDTOs(ags)
	// overlay per-agent binding counts (mcp/skill by id, tools by key) — best-effort.
	if mcp, skill, tools, err := pg.AgentBindingCounts(); err == nil {
		for i := range dtos {
			dtos[i].McpCount = mcp[ags[i].ID]
			dtos[i].SkillCount = skill[ags[i].ID]
			dtos[i].ToolCount = tools[ags[i].Key]
		}
	}
	writeJSON(w, 200, map[string]any{"agents": dtos})
}

// pgCreateAgent creates a CUSTOM conversational agent (builtin=false, role
// 'assistant') and seeds it a starter prompt so its editor isn't blank.
func (s *Server) pgCreateAgent(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct{ Key, Name, Description string }
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Key, req.Name = strings.TrimSpace(req.Key), strings.TrimSpace(req.Name)
	if !reAgentKey.MatchString(req.Key) {
		writeErr(w, 400, "key must start with a lowercase letter and contain only lowercase letters/digits/underscores")
		return
	}
	if req.Name == "" {
		writeErr(w, 400, "name must not be empty")
		return
	}
	if exist, _ := pg.GetAgentByKey(req.Key); exist != nil {
		writeErr(w, 409, "this key already exists")
		return
	}
	a, err := pg.CreateAgent(req.Key, req.Name, req.Description)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// starter prompt so the editor shows something editable from the start.
	if err := pg.SeedPromptIfEmpty(a.ID, agent.DefaultAssistantPrompt); err != nil {
		log.Printf("[agents] seed starter prompt for %s failed: %v", a.Key, err)
	}
	writeJSON(w, 200, agentDTO(a))
}

// pgUpdateAgent updates a custom agent's name/description (built-in agents rejected).
func (s *Server) pgUpdateAgent(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	if a.Builtin {
		writeErr(w, 400, "built-in agents cannot have their name/description changed")
		return
	}
	var req struct{ Name, Description string }
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, 400, "name must not be empty")
		return
	}
	if err := pg.UpdateAgentMeta(a.Key, req.Name, req.Description); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// pgDeleteAgent removes a custom agent (built-in rejected). Prompts/vars/visibility
// cascade via FK; tool-binding cleanup is best-effort.
func (s *Server) pgDeleteAgent(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	if a.Builtin {
		writeErr(w, 400, "built-in agents cannot be deleted")
		return
	}
	if err := pg.DeleteAgent(a.Key); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := pg.RemoveAgentFromToolBindings(a.Key); err != nil {
		log.Printf("[agents] failed to clean up tool bindings of %s: %v", a.Key, err)
	}
	if err := pg.DeleteTriggersForAgent(a.Key); err != nil {
		log.Printf("[agents] failed to clean up triggers of %s: %v", a.Key, err)
	}
	writeJSON(w, 200, map[string]any{"deleted": a.Key})
}

func (s *Server) agentByKey(w http.ResponseWriter, r *http.Request) (*db.DB, *db.Agent, bool) {
	pg := s.pg(w)
	if pg == nil {
		return nil, nil, false
	}
	a, err := pg.GetAgentByKey(r.PathValue("key"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, nil, false
	}
	if a == nil {
		writeErr(w, 404, "agent not found")
		return nil, nil, false
	}
	return pg, a, true
}

// pgSaveAgentConfig updates an agent's runtime config (currently max_turns) and
// re-applies the live LLM so the change takes effect immediately.
func (s *Server) pgSaveAgentConfig(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	// All fields optional (pointers) so a partial patch (e.g. the triggers tab sending
	// only trigger_* fields) leaves the untouched settings alone instead of resetting
	// max_turns/run_seconds to 0.
	var req struct {
		MaxTurns         *int  `json:"max_turns"`
		RunSeconds       *int  `json:"run_seconds"`
		WebSearch        *bool `json:"web_search"`
		InteractiveShell *bool `json:"interactive_shell"`
		// llm_profile_id is tri-state: field absent = unchanged; explicit null = unbind (follow task/global); number = bind that profile.
		LLMProfileID json.RawMessage `json:"llm_profile_id"`
		// P3 trigger post-processing policy (all three optional; providing any writes them as a whole; none provided = unchanged).
		TriggerRunMode     *string `json:"trigger_run_mode"`
		TriggerMergeMode   *string `json:"trigger_merge_mode"`
		TriggerMaxParallel *int    `json:"trigger_max_parallel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	profileChanged := false
	if req.LLMProfileID != nil { // key present (number or null)
		var id *int64
		if err := json.Unmarshal(req.LLMProfileID, &id); err != nil {
			writeErr(w, 400, "malformed llm_profile_id")
			return
		}
		if id != nil { // bind: validate the target profile
			if _, ok := s.loadProfileConfig(*id); !ok {
				writeErr(w, 400, "the specified LLM profile does not exist or is invalid")
				return
			}
		}
		if err := pg.SetAgentLLMProfile(a.Key, id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		profileChanged = true
	}
	if req.MaxTurns != nil {
		mt := *req.MaxTurns
		if mt < 0 {
			mt = 0
		}
		if err := pg.SetAgentMaxTurns(a.Key, mt); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.RunSeconds != nil {
		rs := *req.RunSeconds
		if rs < 0 {
			rs = 0
		}
		if err := pg.SetAgentRunSeconds(a.Key, rs); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.WebSearch != nil {
		if err := pg.SetAgentWebSearch(a.Key, *req.WebSearch); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.InteractiveShell != nil {
		if err := pg.SetAgentInteractiveShell(a.Key, *req.InteractiveShell); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	// P3 trigger policy: the three are written as a group (SetAgentTriggerBehavior writes three columns at once); missing fields are
	// backfilled with current stored values, so passing one doesn't reset the other two to defaults.
	if req.TriggerRunMode != nil || req.TriggerMergeMode != nil || req.TriggerMaxParallel != nil {
		runMode, mergeMode, maxPar := a.TriggerRunMode, a.TriggerMergeMode, a.TriggerMaxParallel
		if req.TriggerRunMode != nil {
			runMode = *req.TriggerRunMode
		}
		if req.TriggerMergeMode != nil {
			mergeMode = *req.TriggerMergeMode
		}
		if req.TriggerMaxParallel != nil {
			maxPar = *req.TriggerMaxParallel
		}
		if err := pg.SetAgentTriggerBehavior(a.Key, runMode, mergeMode, maxPar); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	// an agent's LLM binding change also invalidates pinned-task / per-profile caches
	// so their next round re-resolves each agent's bound model.
	if profileChanged {
		s.invalidateProfileAgents()
	}
	// Task bundles capture operational Agent settings (turn/time budgets, tools,
	// web search). Rebuild them even when no global profile is configured.
	s.invalidateTaskAgents()
	// rebuild the live agents so the new max_turns/run_seconds/binding apply without a restart.
	s.cfgMu.Lock()
	cfg, on := s.llmCfg, s.llmOn
	s.cfgMu.Unlock()
	if on {
		_ = s.applyLLM(cfg)
	}
	resp := map[string]any{"ok": true}
	if req.MaxTurns != nil {
		resp["max_turns"] = *req.MaxTurns
	}
	if req.RunSeconds != nil {
		resp["run_seconds"] = *req.RunSeconds
	}
	writeJSON(w, 200, resp)
}

func (s *Server) pgGetAgent(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	cur, _ := pg.CurrentPrompt(a.ID)
	vars, _ := pg.PromptVars(a.ID)
	vars = withGlobalVars(vars)
	vers, _ := pg.ListPromptVersions(a.ID)
	if vers == nil {
		vers = []db.PromptVersion{}
	}
	mcp, _ := pg.AgentVisible(a.ID, "mcp")
	sk, _ := pg.AgentSkillNames(a.ID)
	if sk == nil {
		sk = []string{}
	}
	// Selectable LLM profiles (id/name/model/is default) for the frontend's "default model" dropdown; the current binding is agent.llm_profile_id.
	profs, _ := pg.ListProfiles()
	llmProfiles := make([]map[string]any, 0, len(profs))
	for _, p := range profs {
		llmProfiles = append(llmProfiles, map[string]any{
			"id": p.ID, "name": p.Name, "model": p.Model, "is_default": p.IsDefault,
		})
	}
	writeJSON(w, 200, map[string]any{
		"agent": agentDTO(a), "prompt": cur, "variables": vars, "versions": vers,
		"visibility":   map[string]any{"mcp": mcp, "skill": sk},
		"llm_profiles": llmProfiles, // bindable LLM profile candidates

		"wrapup_prompt":            a.WrapupPrompt,                  // saved wrap-up prompt (empty = built-in default)
		"wrapup_default":           agent.WrapupDefault(a.Key),      // built-in default (for placeholder/restore default)
		"wrapup_max_turns":         a.WrapupMaxTurns,                // saved wrap-up turns (0 = built-in default)
		"wrapup_max_turns_default": agent.WrapupTurnsDefault(a.Key), // built-in default turns (for the "0 = default N" hint)
		// Task-level timeout wrap-up prompt (only worker/planner have a built-in default; task_timeout_supported lets the frontend decide whether to show the section)
		"task_timeout_wrapup_supported":         agent.TaskTimeoutWrapupDefault(a.Key) != "",
		"task_timeout_wrapup_prompt":            a.TaskTimeoutWrapupPrompt,
		"task_timeout_wrapup_default":           agent.TaskTimeoutWrapupDefault(a.Key),
		"task_timeout_wrapup_max_turns":         a.TaskTimeoutWrapupMaxTurns,
		"task_timeout_wrapup_max_turns_default": agent.WrapupTurnsDefault(a.Key),
	})
}

func (s *Server) pgGetAgentVisibility(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	mcp, _ := pg.AgentVisible(a.ID, "mcp")
	sk, _ := pg.AgentSkillNames(a.ID)
	if sk == nil {
		sk = []string{}
	}
	writeJSON(w, 200, map[string]any{"mcp": mcp, "skill": sk})
}

func (s *Server) pgSetAgentVisibility(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	var body struct {
		MCP   []int64  `json:"mcp"`
		Skill []string `json:"skill"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.SetAgentVisibilityKind(a.ID, "mcp", body.MCP); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := pg.SetAgentSkillVisibility(a.ID, body.Skill); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
