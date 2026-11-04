package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/vulnintel"
	actool "github.com/Autumn-27/norma/tool"
)

const (
	settingVulnSources   = "vulnintel_sources"   // JSON {source: enabled}
	settingVulnKeyPrefix = "vulnintel_key_"      // + source id
	researcherAgentKey   = "researcher"          // preset research agent
	vulnIntelSeedFlag    = "vulnintel_tools_v1"  // one-off default bindings on existing DBs
	researcherSeedFlag   = "researcher_agent_v1" // one-off preset agent creation
)

// vulnIntelAgents receive the vulnerability intelligence tools by default.
var vulnIntelAgents = []string{"mainagent", "planner", "worker", "pentest", "auto", researcherAgentKey}

var (
	vulnIntelOnce sync.Once
	vulnIntelSvc  *vulnintel.Service
)

// vulnIntel returns the process-wide service; settings are read per call so UI
// changes apply without a restart.
func (s *Server) vulnIntel() *vulnintel.Service {
	vulnIntelOnce.Do(func() {
		vulnIntelSvc = vulnintel.New(s.vulnIntelSettings, filepath.Join(s.m.dir, "vulnintel"))
	})
	return vulnIntelSvc
}

func (s *Server) vulnIntelSettings() vulnintel.Settings {
	cfg := vulnintel.Settings{Enabled: map[string]bool{}, Keys: map[string]string{}}
	if raw, ok, _ := s.m.pg.GetSetting(settingVulnSources); ok && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Enabled); err != nil {
			log.Printf("[vulnintel] ignoring malformed %s: %v", settingVulnSources, err)
		}
	}
	for _, src := range vulnintel.Sources {
		if src.Key == "none" {
			continue
		}
		if v, ok, _ := s.m.pg.GetSetting(settingVulnKeyPrefix + src.ID); ok {
			cfg.Keys[src.ID] = v
		}
	}
	// Intelligence lookups are research traffic, not target traffic: they use the
	// web-search egress proxy (empty = direct), never the recording proxy.
	_, _, _, _, cfg.Proxy = s.m.WebSearch()
	return cfg
}

// vulnIntelTools are host tools; ToolResolve keeps them for the agents bound in the tools table.
func (s *Server) vulnIntelTools() []actool.CoreTool {
	return []actool.CoreTool{s.toolVulnLookup(), s.toolCVELookup(), s.toolKEVSearch(), s.toolAskResearcher()}
}

const vulnIntelDiscipline = " Results are vulnerability intelligence, not proof: a listed CVE means the detected version is in a published affected range. " +
	"Confirm the version evidence and, where the rules of engagement allow, exploitability before reporting a finding. " +
	"Check each source status: an empty list only means \"not affected\" when complete=true; skipped/error sources leave gaps that must be stated."

func (s *Server) toolVulnLookup() actool.CoreTool {
	return roTool("vuln_lookup",
		"Look up known vulnerabilities (CVEs and advisories) for fingerprinted software versions in the configured vulnerability databases "+
			"(NVD, OSV, Wordfence, WPScan), enriched with known-exploited status (CISA KEV, ENISA EU KEV, VulnCheck KEV) and EPSS exploit probability. "+
			"Call it as soon as a product/plugin/package version is identified. Results are sorted with KEV-listed and highest CVSS first."+vulnIntelDiscipline,
		objSchema(map[string]any{
			"components": map[string]any{
				"type":        "array",
				"description": fmt.Sprintf("Software to check (max %d per call).", vulnintel.MaxComponents),
				"items": objSchema(map[string]any{
					"kind": map[string]any{"type": "string", "enum": []string{"wordpress_core", "wordpress_plugin", "wordpress_theme", "product", "package"},
						"description": "wordpress_core (name may be empty), wordpress_plugin/wordpress_theme (name = slug, e.g. revslider, or display name), product (server software, CMS, appliance: matched via NVD CPE), package (library: matched via OSV, needs ecosystem)"},
					"name":      strParam("product name, WordPress slug or package name, e.g. nginx, OpenSSH, revslider, jquery"),
					"version":   strParam("detected version, e.g. 1.18.0; omit to list every known vulnerability"),
					"vendor":    strParam("optional CPE vendor to disambiguate a product, e.g. f5 for nginx"),
					"ecosystem": strParam("package ecosystem for kind=package: npm, PyPI, Packagist, Maven, Go, RubyGems, crates.io, NuGet, Debian, Alpine …"),
				}, "kind"),
			},
		}, "components"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Components []vulnintel.Component `json:"components"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			if len(a.Components) == 0 {
				return actool.Errorf("components must not be empty"), nil
			}
			return jsonResult(map[string]any{"results": s.vulnIntel().Lookup(ctx, a.Components)})
		})
}

func (s *Server) toolCVELookup() actool.CoreTool {
	return roTool("cve_lookup",
		"Get details for vulnerability ids (CVE-…, GHSA-…, other OSV ids): description, CVSS, CWE, affected products/version ranges, "+
			"known-exploited listings (CISA / EU / VulnCheck KEV with due dates and ransomware use), EPSS, exploit and advisory references."+vulnIntelDiscipline,
		objSchema(map[string]any{
			"ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": fmt.Sprintf("vulnerability ids, max %d", vulnintel.MaxCVEs)},
		}, "ids"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IDs []string `json:"ids"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			if len(a.IDs) == 0 {
				return actool.Errorf("ids must not be empty"), nil
			}
			return jsonResult(map[string]any{"results": s.vulnIntel().CVEDetails(ctx, a.IDs)})
		})
}

func (s *Server) toolKEVSearch() actool.CoreTool {
	return roTool("kev_search",
		"Search the known-exploited vulnerability catalogs (CISA KEV, ENISA EU KEV; VulnCheck KEV for exact CVE ids) by keyword, vendor, product, "+
			"date added or ransomware use. Use it to see which issues of a product are actively exploited in the wild, or what was added recently.",
		objSchema(map[string]any{
			"query":           strParam("keywords matched against CVE id, vendor, product, name and description (all must match)"),
			"vendor":          strParam("vendor substring, e.g. Fortinet"),
			"product":         strParam("product substring, e.g. FortiOS"),
			"since":           strParam("only entries added on/after this date (YYYY-MM-DD)"),
			"ransomware_only": map[string]any{"type": "boolean", "description": "only entries with known ransomware campaign use"},
			"catalogs": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{vulnintel.SourceCISAKEV, vulnintel.SourceEUKEV, vulnintel.SourceVulnCheckKEV}},
				"description": "restrict to these catalogs (default: all enabled)"},
			"limit": map[string]any{"type": "integer", "description": "max entries (default 50, max 200), newest first"},
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var q vulnintel.KEVQuery
			if err := json.Unmarshal(in, &q); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			return jsonResult(s.vulnIntel().KEVSearch(ctx, q))
		})
}

// researchTimeout bounds one ask_researcher call when the agent has no run budget configured.
const researchTimeout = 15 * time.Minute

func (s *Server) toolAskResearcher() actool.CoreTool {
	return wrTool("ask_researcher",
		"Delegate an internet research question to the research agent, which searches the web, reads sources and queries the vulnerability "+
			"databases, then answers with cited sources. Use it for questions your own tools cannot answer: vendor advisories, exploit/PoC "+
			"availability, patch notes, default credentials, product documentation, technology background. Blocks until the answer is ready "+
			"(usually 1–5 minutes); the full research run is kept as a conversation of the research agent. Ask one focused question with the "+
			"concrete facts it needs (product, version, observed behaviour).",
		objSchema(map[string]any{
			"question": strParam("the research question"),
			"context":  strParam("relevant facts already known (detected versions, URLs, observed behaviour); never include credentials"),
		}, "question"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Question string `json:"question"`
				Context  string `json:"context"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			if strings.TrimSpace(a.Question) == "" {
				return actool.Errorf("question must not be empty"), nil
			}
			answer, convID, err := s.askResearcher(ctx, a.Question, a.Context, agent.RunInfoFrom(ctx).TaskID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"answer": answer, "conversation_id": convID})
		})
}

// askResearcher runs the research agent in its own conversation and returns its final answer.
func (s *Server) askResearcher(ctx context.Context, question, extra string, taskID int64) (string, int64, error) {
	a, err := s.m.pg.GetAgentByKey(researcherAgentKey)
	if err != nil || a == nil {
		return "", 0, fmt.Errorf("the research agent %q does not exist; recreate it under System → Agents", researcherAgentKey)
	}
	if !a.Enabled {
		return "", 0, fmt.Errorf("the research agent is disabled")
	}
	var b strings.Builder
	if taskID > 0 {
		if t, ok := s.m.Task(fmt.Sprint(taskID)); ok && t != nil {
			if h := taskContextHeader(taskID, t.descriptionText(), t.goalText()); h != "" {
				b.WriteString(h + "\n\n")
			}
		}
	}
	b.WriteString("Research question:\n" + strings.TrimSpace(question))
	if strings.TrimSpace(extra) != "" {
		b.WriteString("\n\nKnown context:\n" + strings.TrimSpace(extra))
	}
	msg := b.String()
	title := firstLine(question, 60)
	if taskID > 0 {
		title = fmt.Sprintf("Task #%d · %s", taskID, title)
	}
	c, err := s.m.pg.CreateConversation(researcherAgentKey, title, nil)
	if err != nil || c == nil {
		return "", 0, fmt.Errorf("create research conversation: %v", err)
	}
	if _, err := s.m.pg.AppendConvActivity(c.ID, db.Activity{Worker: researcherAgentKey, Kind: "user", Summary: firstLine(msg, 200), Detail: msg}); err != nil {
		log.Printf("[researcher] append question failed: %v", err)
	}
	busyKey := s.convBusyKey(c.ID)
	s.chatMu.Lock()
	s.chatBusy[busyKey] = true
	s.chatMu.Unlock()
	runCtx, cancel := s.conversationRunContext(c.ID, busyKey)
	// The caller's cancellation (pause, kill, its own timeout) stops the research too.
	stop := context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
	defer stop()
	if a.RunSecs <= 0 {
		var timeoutCancel context.CancelFunc
		runCtx, timeoutCancel = context.WithTimeout(runCtx, researchTimeout)
		defer timeoutCancel()
	}
	answer := s.runConversationTurn(runCtx, cancel, c, msg, busyKey)
	if strings.TrimSpace(answer) == "" {
		if ctx.Err() != nil {
			return "", c.ID, fmt.Errorf("research cancelled: %v", context.Cause(ctx))
		}
		return "", c.ID, fmt.Errorf("the research agent returned no answer; see conversation #%d", c.ID)
	}
	return answer, c.ID, nil
}

// seedVulnIntelTools seeds the host tools into the tools table. SeedTool is
// first-insert-only; the flag-guarded binding pass gives existing rows their
// default agents once without overriding later manual unbinds.
func (s *Server) seedVulnIntelTools() {
	agents, _ := json.Marshal(vulnIntelAgents)
	main, _ := json.Marshal([]string{"mainagent", "planner"})
	for _, t := range s.vulnIntelTools() {
		schema, _ := json.Marshal(t.InputSchema())
		bind := agents
		if t.Name() == "ask_researcher" {
			bind = main // workers stay focused on executing their intent
		}
		if err := s.m.pg.SeedTool(t.Name(), t.Description(), schema, bind); err != nil {
			log.Printf("[vulnintel] seed %s failed: %v", t.Name(), err)
		}
	}
	if v, _, _ := s.m.pg.GetSetting(vulnIntelSeedFlag); v == "true" {
		return
	}
	for _, key := range []string{"vuln_lookup", "cve_lookup", "kev_search"} {
		for _, ag := range vulnIntelAgents {
			if err := s.m.pg.AddAgentToToolBinding(ag, []string{key}); err != nil {
				log.Printf("[vulnintel] bind %s to %s failed: %v", key, ag, err)
				return
			}
		}
	}
	_ = s.m.pg.SetSetting(vulnIntelSeedFlag, "true")
}

// seedResearcherAgent creates the preset research agent once, with web search on.
func (s *Server) seedResearcherAgent() {
	if v, _, _ := s.m.pg.GetSetting(researcherSeedFlag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(researcherSeedFlag, "true") }()
	if exist, _ := s.m.pg.GetAgentByKey(researcherAgentKey); exist != nil {
		return // key already taken by a user-created agent
	}
	a, err := s.m.pg.CreateAgent(researcherAgentKey, "Researcher",
		"Internet and vulnerability research: searches the web, reads sources and queries vulnerability databases, answering with cited sources.")
	if err != nil {
		log.Printf("[researcher] create agent failed: %v", err)
		return
	}
	if err := s.m.pg.SeedPromptIfEmpty(a.ID, agent.ResearcherDefaultPrompt); err != nil {
		log.Printf("[researcher] seed prompt failed: %v", err)
	}
	if err := s.m.pg.SetAgentWebSearch(researcherAgentKey, true); err != nil {
		log.Printf("[researcher] enable web search failed: %v", err)
	}
	log.Printf("[researcher] preset research agent created")
}

// ---- settings API ----

type vulnSourceDTO struct {
	vulnintel.SourceInfo
	Enabled bool `json:"enabled"`
	KeySet  bool `json:"key_set"`
}

func (s *Server) vulnIntelPayload() map[string]any {
	cfg := s.vulnIntelSettings()
	out := make([]vulnSourceDTO, 0, len(vulnintel.Sources))
	for _, src := range vulnintel.Sources {
		on, ok := cfg.Enabled[src.ID]
		out = append(out, vulnSourceDTO{SourceInfo: src, Enabled: !ok || on, KeySet: strings.TrimSpace(cfg.Keys[src.ID]) != ""})
	}
	return map[string]any{"sources": out, "cache": s.vulnIntel().CacheStats()}
}

// clearVulnIntelCache drops cached lookup answers and downloaded feeds.
func (s *Server) clearVulnIntelCache(w http.ResponseWriter, r *http.Request) {
	if err := s.vulnIntel().ClearCache(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, s.vulnIntelPayload())
}

func (s *Server) getVulnIntel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.vulnIntelPayload())
}

// putVulnIntel updates per-source switches and keys. A key of null leaves the
// stored key untouched, "" clears it; keys are never echoed back.
func (s *Server) putVulnIntel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled map[string]bool    `json:"enabled"`
		Keys    map[string]*string `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	for id := range req.Enabled {
		if _, ok := vulnintel.SourceByID(id); !ok {
			writeErr(w, 400, "unknown source "+id)
			return
		}
	}
	for id := range req.Keys {
		if info, ok := vulnintel.SourceByID(id); !ok || info.Key == "none" {
			writeErr(w, 400, "source "+id+" takes no API key")
			return
		}
	}
	if len(req.Enabled) > 0 {
		cur := s.vulnIntelSettings().Enabled
		for id, on := range req.Enabled {
			cur[id] = on
		}
		raw, _ := json.Marshal(cur)
		if err := s.m.pg.SetSetting(settingVulnSources, string(raw)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	for id, key := range req.Keys {
		if key == nil {
			continue
		}
		if err := s.m.pg.SetSetting(settingVulnKeyPrefix+id, strings.TrimSpace(*key)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, s.vulnIntelPayload())
}

// testVulnIntel checks one source; an unsaved key from the form may be supplied.
func (s *Server) testVulnIntel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
		Key    string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	start := time.Now()
	detail, err := s.vulnIntel().TestSource(ctx, req.Source, req.Key)
	res := map[string]any{"ok": err == nil, "detail": detail, "elapsed_ms": time.Since(start).Milliseconds()}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, 200, res)
}
