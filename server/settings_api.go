package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// getSettings returns the runtime app settings the UI toggles. The Brave API key
// is returned as a boolean presence flag (brave_key_set), never the value itself,
// so the UI can show "configured" without echoing the secret back.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.settingsPayload())
}

func (s *Server) settingsPayload() map[string]any {
	reviewOptions := s.completionReviewOptions()
	on, backend, braveKey, tavilyKey, proxy := s.m.WebSearch()
	pyStored, _, _ := s.m.pg.GetSetting(settingPythonInterp)
	concOn, concLimit := s.m.ConcurrencyLimit()
	if concLimit == 0 {
		concLimit = defaultConcurrencyLimit // echo a sensible default to the UI even when disabled
	}
	return map[string]any{
		"completion_review_seconds": int(reviewOptions.Duration / time.Second),
		"completion_review_turns":   reviewOptions.MaxTurns,
		"traffic_capture":           s.m.TrafficEnabled(),
		"agent_traffic_binding":     s.m.pg.GetBool(settingAgentTrafficBinding, false),
		"llm_record":                s.m.LLMRecordEnabled(),
		"web_search_enabled":        on,
		"web_search_backend":        backend,
		"brave_key_set":             strings.TrimSpace(braveKey) != "",
		"tavily_key_set":            strings.TrimSpace(tavilyKey) != "",
		"web_search_proxy":          proxy,                       // separate egress proxy (http/https/socks5), empty = direct
		"global_proxy":              s.m.GlobalProxy(),           // global egress proxy (http/https/socks5) for all target traffic, empty = direct
		"python_interpreter":        strings.TrimSpace(pyStored), // value set by the user/auto-detection (empty = runtime detection)
		"workers":                   s.m.Workers(),               // number of concurrent worker agents (default 3); applies to tasks started afterwards
		"task_concurrency_enabled":  concOn,                      // task concurrency limit switch (off by default)
		"task_concurrency_limit":    concLimit,                   // max concurrently running tasks (default 5 when enabled)
		// LLM rotation (failover). Off by default; when on, agents using the global active profile switch to the next profile
		// automatically when the current one is unavailable. bind_fallback only matters while rotation is on (off by default).
		"llm_pool_enabled":       s.m.LLMPoolEnabled(),
		"llm_pool_bind_fallback": s.m.LLMPoolBindFallback(),
		// Operating-constraint injection scope (all on by default): splices the task's allow/deny constraints into the matching agent's system prompt.
		"constraints_inject_planner": s.constraintInjectPlanner(),
		"constraints_inject_worker":  s.constraintInjectWorker(),
		// Experimental: noa model-driven context compaction (off by default). When on, the four kinds of platform agents have context compaction
		// taken over by noa, replacing the built-in compaction; read once per run, applies to runs started afterwards.
		"noa_compaction": s.m.NoaCompactionEnabled(),
		// Global settings for finding IM notifications. Channels themselves are separate resources managed via /api/notify/*;
		// only the three settings that "apply to all channels" live here.
		"notify_enabled":             s.m.pg.GetBool(settingNotifyEnabled, true),
		"notify_public_base_url":     notifyPublicBaseURL(s.m.pg),
		"notify_digest_interval_min": notifyDigestIntervalMin(s.m.pg),
	}
}

// notifyPublicBaseURL reads the external base URL used for notification backlinks.
func notifyPublicBaseURL(pg *db.DB) string {
	v, _, _ := pg.GetSetting(settingNotifyPublicBaseURL)
	return v
}

// notifyDigestIntervalMin reads the digest interval (minutes), falling back to the default when invalid or unset.
// Echo the default rather than an empty string so the UI can fill the effective value into the input.
func notifyDigestIntervalMin(pg *db.DB) int {
	v, ok, _ := pg.GetSetting(settingNotifyDigestMinutes)
	if !ok {
		return notifyDefaultDigestMinutes
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return notifyDefaultDigestMinutes
	}
	return n
}

// pgDetectPython re-runs interpreter detection, stores + returns it.
func (s *Server) pgDetectPython(w http.ResponseWriter, r *http.Request) {
	p := detectPython()
	if p == "" {
		writeErr(w, 404, "python not found (neither python3 nor python is on PATH)")
		return
	}
	if err := s.m.pg.SetSetting(settingPythonInterp, p); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"python_interpreter": p})
}

// putSettings applies a settings change. Toggling traffic_capture rebuilds the
// agents (applyLLM) so the new proxy/traffic-tools/prompt state takes hold — when
// off, agents get no proxy config, no traffic tools, and no proxy prompt content.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompletionReviewSeconds *int  `json:"completion_review_seconds"`
		CompletionReviewTurns   *int  `json:"completion_review_turns"`
		TrafficCapture          *bool `json:"traffic_capture"`
		AgentTrafficBinding     *bool `json:"agent_traffic_binding"`
		LLMRecord               *bool `json:"llm_record"` // LLM recording switch (off by default); takes effect immediately, no agent rebuild needed
		// Web search. WebSearchEnabled/Backend toggle the tool + backend; BraveKey/TavilyKey
		// are optional — omit (null) to leave a stored key untouched, send "" to clear.
		WebSearchEnabled *bool   `json:"web_search_enabled"`
		WebSearchBackend *string `json:"web_search_backend"`
		BraveKey         *string `json:"brave_search_api_key"`
		TavilyKey        *string `json:"tavily_search_api_key"`
		WebSearchProxy   *string `json:"web_search_proxy"`   // separate egress proxy (http/https/socks5); null = unchanged, "" = clear
		GlobalProxy      *string `json:"global_proxy"`       // global egress proxy (http/https/socks5); null = unchanged, "" = clear (direct)
		PythonInterp     *string `json:"python_interpreter"` // python interpreter path for custom script tools
		Workers          *int    `json:"workers"`            // number of concurrent worker agents (>0); applies to tasks started afterwards
		// Task concurrency limit: cap on tasks "running" at the same time. Off = unlimited; when on, new tasks over the limit queue and start automatically when a slot frees up.
		ConcurrencyEnabled *bool `json:"task_concurrency_enabled"`
		ConcurrencyLimit   *int  `json:"task_concurrency_limit"`
		// LLM rotation (failover) switch + "fall back to the rotation chain when a bound profile fails" switch. Both need
		// the provider chain rebuilt to take effect, via the changed → applyLLM path below.
		LLMPoolEnabled      *bool `json:"llm_pool_enabled"`
		LLMPoolBindFallback *bool `json:"llm_pool_bind_fallback"`
		// Operating-constraint injection scope switches (all on by default); take effect immediately (planner/worker read them every round), no agent rebuild needed.
		ConstraintsInjectPlanner *bool `json:"constraints_inject_planner"`
		ConstraintsInjectWorker  *bool `json:"constraints_inject_worker"`
		// Experimental: noa context compaction switch (off by default); read per run, applies to runs started afterwards, no agent rebuild needed.
		NoaCompaction *bool `json:"noa_compaction"`
		// Global settings for finding IM notifications. All three are read by the delivery engine every round, so changes take effect immediately
		// without rebuilding agents or restarting.
		NotifyEnabled    *bool   `json:"notify_enabled"`
		NotifyBaseURL    *string `json:"notify_public_base_url"`
		NotifyDigestMins *int    `json:"notify_digest_interval_min"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.CompletionReviewSeconds != nil && (*req.CompletionReviewSeconds < 15 || *req.CompletionReviewSeconds > 900) {
		writeErr(w, 400, "final verification budget must be 15–900 seconds")
		return
	}
	if req.CompletionReviewTurns != nil && (*req.CompletionReviewTurns < 1 || *req.CompletionReviewTurns > 64) {
		writeErr(w, 400, "final verification rounds must be 1–64")
		return
	}
	for key, value := range map[string]*int{settingCompletionReviewSeconds: req.CompletionReviewSeconds, settingCompletionReviewTurns: req.CompletionReviewTurns} {
		if value != nil {
			if err := s.m.pg.SetSetting(key, strconv.Itoa(*value)); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
	}
	if req.CompletionReviewSeconds != nil || req.CompletionReviewTurns != nil {
		for _, task := range s.m.List() {
			task.Notify()
		}
	}
	if req.ConstraintsInjectPlanner != nil {
		if err := s.m.pg.SetBool(settingConstraintsInjectPlanner, *req.ConstraintsInjectPlanner); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.ConstraintsInjectWorker != nil {
		if err := s.m.pg.SetBool(settingConstraintsInjectWorker, *req.ConstraintsInjectWorker); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.NoaCompaction != nil {
		// Resolver read per run; switching applies immediately to runs started afterwards, no applyLLM rebuild needed.
		if err := s.m.SetNoaCompaction(*req.NoaCompaction); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	// Global notification settings: the delivery engine rereads them every round, so they take effect immediately without a restart.
	if req.NotifyEnabled != nil {
		if err := s.m.pg.SetBool(settingNotifyEnabled, *req.NotifyEnabled); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.NotifyBaseURL != nil {
		// Always trim the trailing slash: backlinks are built with fmt.Sprintf("%s/function/..."),
		// and a trailing slash would produce double-slash paths like "//function/...".
		base := trimTrailingSlash(strings.TrimSpace(*req.NotifyBaseURL))
		if base != "" && !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			writeErr(w, 400, "backlink URL must start with http:// or https://")
			return
		}
		if err := s.m.pg.SetSetting(settingNotifyPublicBaseURL, base); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.NotifyDigestMins != nil {
		// Floor of 1 minute: a shorter interval is effectively real-time push, in which case the channel should simply switch to realtime mode.
		if *req.NotifyDigestMins < 1 || *req.NotifyDigestMins > 24*60 {
			writeErr(w, 400, "digest interval must be between 1 and 1440 minutes")
			return
		}
		if err := s.m.pg.SetSetting(settingNotifyDigestMinutes, strconv.Itoa(*req.NotifyDigestMins)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.Workers != nil {
		if err := s.m.SetWorkers(*req.Workers); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if req.ConcurrencyEnabled != nil || req.ConcurrencyLimit != nil {
		// Partial PUT: missing fields fall back to the current values, so changing one doesn't reset the other.
		curOn, curLimit := s.m.ConcurrencyLimit()
		if curLimit == 0 {
			curLimit = defaultConcurrencyLimit
		}
		on, limit := curOn, curLimit
		if req.ConcurrencyEnabled != nil {
			on = *req.ConcurrencyEnabled
		}
		if req.ConcurrencyLimit != nil {
			limit = *req.ConcurrencyLimit
		}
		if err := s.m.SetConcurrency(on, limit); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		// Reconcile immediately: when disabled release everything queued, when the limit is raised start queued tasks, without waiting for the next tick.
		go s.reconcileConcurrency()
	}
	if req.PythonInterp != nil {
		if err := s.m.pg.SetSetting(settingPythonInterp, strings.TrimSpace(*req.PythonInterp)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.LLMRecord != nil {
		// The recorder reads this flag on every call, so switching takes effect immediately without an applyLLM rebuild.
		if err := s.m.SetLLMRecordEnabled(*req.LLMRecord); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	changed := false
	if req.LLMPoolEnabled != nil {
		if err := s.m.SetLLMPoolEnabled(*req.LLMPoolEnabled); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		changed = true // the provider chain itself changes shape → rebuild
	}
	if req.LLMPoolBindFallback != nil {
		if err := s.m.SetLLMPoolBindFallback(*req.LLMPoolBindFallback); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		changed = true
	}
	if req.LLMPoolEnabled != nil || req.LLMPoolBindFallback != nil {
		// Pinned tasks' planner/worker and per-profile chat agents hold providers
		// built under the OLD switch state — drop them so they pick up the new one.
		s.invalidateProfileAgents()
	}
	if req.AgentTrafficBinding != nil {
		if err := s.m.pg.SetBool(settingAgentTrafficBinding, *req.AgentTrafficBinding); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.TrafficCapture != nil {
		if err := s.m.SetTrafficEnabled(*req.TrafficCapture); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		changed = true
	}
	if req.GlobalProxy != nil {
		// Validation failure (bad scheme/host) is a client error, not a 500.
		if err := s.m.SetGlobalProxy(*req.GlobalProxy); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		changed = true // capture-off egress is baked into agents at build time → rebuild
	}
	if req.WebSearchEnabled != nil || req.WebSearchBackend != nil || req.BraveKey != nil || req.TavilyKey != nil || req.WebSearchProxy != nil {
		// Fill unspecified fields from current state so a partial PUT doesn't reset them.
		on, backend, _, _, _ := s.m.WebSearch()
		if req.WebSearchEnabled != nil {
			on = *req.WebSearchEnabled
		}
		if req.WebSearchBackend != nil {
			backend = *req.WebSearchBackend
		}
		if err := s.m.SetWebSearch(on, backend, req.BraveKey, req.TavilyKey, req.WebSearchProxy); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		changed = true
	}
	if changed {
		// rebuild agents so the new proxy/tools/prompt/web-search take hold (only if LLM configured).
		s.cfgMu.Lock()
		cfg, on := s.llmCfg, s.llmOn
		s.cfgMu.Unlock()
		if on {
			if err := s.applyLLM(cfg); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
	}
	writeJSON(w, 200, s.settingsPayload())
}

// testWebSearch runs a real "test" search with the given (or currently saved)
// backend/proxy/key to verify the config can actually reach a search backend —
// mirroring testLLM. Backend/proxy come from the request (so the form's unsaved
// edits are tested); empty API keys fall back to stored values so the user need
// not retype them. Always 200 with {ok, error?, count?, backend?}.
func (s *Server) testWebSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Backend   string `json:"web_search_backend"`
		Proxy     string `json:"web_search_proxy"`
		BraveKey  string `json:"brave_search_api_key"`
		TavilyKey string `json:"tavily_search_api_key"`
	}
	// Empty body is fine — fall back entirely to the saved config below.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErr(w, 400, err.Error())
		return
	}
	_, backend, storedBraveKey, storedTavilyKey, _ := s.m.WebSearch()
	if strings.TrimSpace(req.Backend) != "" {
		backend = req.Backend
	}
	// Proxy is taken from the form as-is (empty = direct), so testing reflects exactly
	// what's shown — including an intentional "clear proxy to test direct" before saving.
	proxy := strings.TrimSpace(req.Proxy)
	// API keys are secrets the form omits when already saved, so fall back to stored.
	braveKey := storedBraveKey
	if strings.TrimSpace(req.BraveKey) != "" {
		braveKey = req.BraveKey
	}
	tavilyKey := storedTavilyKey
	if strings.TrimSpace(req.TavilyKey) != "" {
		tavilyKey = req.TavilyKey
	}
	cfg := actool.WebSearchConfig{Backend: backend, BraveAPIKey: braveKey, TavilyAPIKey: tavilyKey, Proxy: proxy}
	// Hard cap so a slow/blocked proxy can't hang the request.
	wall := 30 * time.Second
	// deepseek's credentials are not in the form; they come from the currently active LLM profile. It also runs model inference
	// on every search, so the generic 30s cap is too tight and gets relaxed here. We don't pre-check whether the config works: this test
	// is exactly how users confirm it themselves, and the error below is more informative than a pre-check when it really fails.
	probeQuery := "test"
	if strings.TrimSpace(backend) == deepSeekWebSearchBackend {
		cfg.DeepSeekBaseURL, cfg.DeepSeekAPIKey, cfg.DeepSeekModel = s.m.deepSeekSearchCreds()
		wall = 120 * time.Second
		// The DeepSeek-side model decides the search query itself; "test" is too vague and makes it answer without searching.
		probeQuery = "DeepSeek company official website"
	}
	ctx, cancel := context.WithTimeout(r.Context(), wall)
	defer cancel()
	results, err := actool.WebSearchProbe(ctx, cfg, probeQuery, 3)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error(), "backend": backend})
		return
	}
	if len(results) == 0 {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "search returned 0 results (possibly rate limited or the proxy is unreachable)", "backend": backend})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "count": len(results), "backend": backend})
}
