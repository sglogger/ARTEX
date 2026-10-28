package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
)

// getLLM returns the current LLM config (key never exposed).
func (s *Server) getLLM(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	writeJSON(w, 200, map[string]any{
		"configured":       s.llmOn,
		"provider":         s.llmCfg.Provider(),
		"model":            s.llmCfg.Model,
		"base_url":         s.llmCfg.BaseURL,
		"proxy":            s.llmCfg.Proxy,
		"key_set":          s.llmCfg.APIKey != "",
		"rate_per_second":  s.llmCfg.RatePerSecond,
		"rate_per_minute":  s.llmCfg.RatePerMinute,
		"context_window_k": s.llmCfg.ContextWindowK,
		"thinking_type":    s.llmCfg.ThinkingType,
		"reasoning_effort": s.llmCfg.ReasoningEffort,
	})
}

// setLLM configures the LLM at runtime. A blank api_key keeps the existing key.
func (s *Server) setLLM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider        string  `json:"provider"`
		Model           string  `json:"model"`
		BaseURL         string  `json:"base_url"`
		Proxy           string  `json:"proxy"`
		APIKey          string  `json:"api_key"`
		RatePerSecond   float64 `json:"rate_per_second"`
		RatePerMinute   float64 `json:"rate_per_minute"`
		ContextWindowK  int     `json:"context_window_k"`
		ThinkingType    string  `json:"thinking_type"`
		ReasoningEffort string  `json:"reasoning_effort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cfg := agent.ConfigFrom(req.Provider, req.Model, req.BaseURL, req.APIKey, req.Proxy)
	cfg.RatePerSecond, cfg.RatePerMinute = req.RatePerSecond, req.RatePerMinute
	cfg.ThinkingType = req.ThinkingType
	cfg.ReasoningEffort = req.ReasoningEffort
	if k := req.ContextWindowK; k > 0 { // 0 = keep default (200K); cap at 1M
		if k > 1000 {
			k = 1000
		}
		cfg.ContextWindowK = k
	}
	if cfg.APIKey == "" {
		s.cfgMu.Lock()
		cfg.APIKey = s.llmCfg.APIKey // keep existing key if not re-entered
		s.cfgMu.Unlock()
	}
	if cfg.APIKey == "" {
		writeErr(w, 400, "api_key required")
		return
	}
	// Validate provider construction before persisting it as the active profile.
	if _, err := cfg.NewProvider(); err != nil {
		writeErr(w, 400, "provider init failed: "+err.Error())
		return
	}
	if err := s.saveLLMConfig(cfg); err != nil {
		writeErr(w, 500, "persist provider failed: "+err.Error())
		return
	}
	s.invalidateProfileAgents()
	if _, ok := s.loadLLMConfig(); !ok {
		writeErr(w, 500, "saved provider is unavailable")
		return
	}
	if err := s.applyLLM(cfg); err != nil {
		writeErr(w, 400, "provider init failed: "+err.Error())
		return
	}
	log.Printf("[engine] LLM configured via UI: %s / %s", cfg.Provider(), cfg.Model)
	s.getLLM(w, r)
}

// testLLM makes a real minimal completion to verify the config works.
func (s *Server) testLLM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider         string `json:"provider"`
		Model            string `json:"model"`
		BaseURL          string `json:"base_url"`
		Proxy            string `json:"proxy"`
		APIKey           string `json:"api_key"`
		ThinkingType     string `json:"thinking_type"`
		ReasoningEffort  string `json:"reasoning_effort"`
		ProfileID        *int64 `json:"profile_id"`         // passed when testing a saved profile: if api_key is empty its stored key is used
		Streaming        *bool  `json:"streaming"`          // omitted = streaming, same default as when saving a profile
		SessionHeaderKey string `json:"session_header_key"` // non-empty = the test also sends this custom session header, valued with a one-off random session id
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cfg := agent.ConfigFrom(req.Provider, req.Model, req.BaseURL, req.APIKey, req.Proxy)
	// mirror production: send the SAME thinking params so a provider that rejects the
	// reasoning_effort/thinking field fails the test too (no false "test ok, run 400").
	cfg.ThinkingType = req.ThinkingType
	cfg.ReasoningEffort = req.ReasoningEffort
	// Likewise the transport mode follows the profile's choice: endpoints supporting only one channel must be exposed
	// here, not discovered in a conversation as "the config that passed the test doesn't run at all".
	if req.Streaming != nil {
		cfg.Stream = *req.Streaming
	}
	// The custom session header name follows the profile: when non-empty the test request sends this header too (valued with a one-off random session id,
	// see TestConnection). Endpoints like opencode zen that require x-opencode-session return 400 without it,
	// so it must be sent on the test path too, otherwise "chat works, test returns 400".
	cfg.SessionHeaderKey = req.SessionHeaderKey
	// API key resolution priority: form input > key stored in the given profile > key from the global config.
	// Saved profile keys are never sent back to the browser, so the form is empty when testing a saved profile and the key has to come from the DB.
	// Same for the session header name: fall back to the saved profile's value when the form omits it.
	if req.ProfileID != nil && (cfg.APIKey == "" || cfg.SessionHeaderKey == "") {
		if p, err := s.m.pg.ProfileByID(*req.ProfileID); err == nil && p != nil {
			if cfg.APIKey == "" {
				cfg.APIKey = p.APIKey
			}
			if cfg.SessionHeaderKey == "" {
				cfg.SessionHeaderKey = p.SessionHeaderKey
			}
		}
	}
	if cfg.APIKey == "" {
		s.cfgMu.Lock()
		cfg.APIKey = s.llmCfg.APIKey
		s.cfgMu.Unlock()
	}
	if cfg.APIKey == "" {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "no API key provided"})
		return
	}
	// Retry parameters are [not] applied to the connection test: the test has a 30s hard timeout, and stacking configured retry counts/long intervals
	// would only make a working endpoint fail the test with a "timeout". The test checks "is this endpoint reachable"; retry cadence
	// matters once things are running.
	lat, reply, err := agent.TestConnection(r.Context(), cfg)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Return the model's actual reply so "test passed" is verifiable: you can see it really said something, not just HTTP 200.
	writeJSON(w, 200, map[string]any{
		"ok": true, "latency_ms": lat.Milliseconds(), "model": cfg.Model, "reply": truncateReply(reply),
	})
}

// truncateReply clips a connection-test reply for display. A model told to answer
// "OK" can still ramble (or think out loud); the UI only needs enough to show it
// really said something. Rune-based so multibyte text never splits mid-character.
func truncateReply(s string) string {
	const max = 200
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// pgListModels fetches available models from the provider's API endpoint.
// Supports OpenAI-format (GET /models) and Anthropic-format (GET /v1/models); for
// Anthropic-compatible third parties (e.g. DeepSeek) whose model list lives only on
// the OpenAI path, it falls back to the OpenAI endpoint at the stripped root.
func (s *Server) pgListModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider  string `json:"provider"` // "openai" | "anthropic"
		BaseURL   string `json:"base_url"`
		APIKey    string `json:"api_key"`
		Proxy     string `json:"proxy"`
		ProfileID *int64 `json:"profile_id"` // fallback: use stored key from this profile
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// Resolve API key: form input > profile stored key.
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" && req.ProfileID != nil {
		if p, err := s.m.pg.ProfileByID(*req.ProfileID); err == nil && p != nil {
			apiKey = p.APIKey
		}
	}
	if apiKey == "" {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "no API key provided"})
		return
	}

	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	provider := strings.TrimSpace(req.Provider)

	// Candidate endpoints to try in order. Some Anthropic-compatible providers
	// (e.g. DeepSeek) implement /v1/messages under an /anthropic path but expose
	// the model list only on their OpenAI-format path — so for anthropic we fall
	// back to the OpenAI endpoint at the stripped root.
	type candidate struct {
		url string
		hdr http.Header
	}
	bearerHdr := func() http.Header {
		h := http.Header{}
		h.Set("Authorization", "Bearer "+apiKey)
		return h
	}
	anthropicHdr := func() http.Header {
		h := http.Header{}
		h.Set("x-api-key", apiKey)
		h.Set("anthropic-version", "2023-06-01")
		return h
	}

	var candidates []candidate
	switch provider {
	case "openai", "openai-responses":
		// Responses API shares the OpenAI model list at /v1/models; tolerate a full
		// endpoint URL of either format.
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		b := strings.TrimRight(strings.TrimSuffix(strings.TrimSuffix(baseURL, "/chat/completions"), "/responses"), "/")
		candidates = append(candidates, candidate{b + "/models", bearerHdr()})
	default: // anthropic
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}
		b := strings.TrimRight(strings.TrimSuffix(baseURL, "/v1/messages"), "/")
		candidates = append(candidates, candidate{b + "/v1/models", anthropicHdr()})
		// Fallback for Anthropic-compatible third parties whose model list lives on
		// the OpenAI path: strip a trailing /anthropic and try the OpenAI endpoint.
		if root := strings.TrimRight(strings.TrimSuffix(b, "/anthropic"), "/"); root != b {
			candidates = append(candidates,
				candidate{root + "/models", bearerHdr()},
				candidate{root + "/v1/models", bearerHdr()},
			)
		}
	}

	// Build HTTP client with optional proxy.
	transport := &http.Transport{}
	if p := strings.TrimSpace(req.Proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			transport.Proxy = http.ProxyURL(pu)
		}
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	// Try each candidate; return the first that yields a non-empty model list.
	var lastErr string
	emptyOK := false
	for _, c := range candidates {
		httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.url, nil)
		if err != nil {
			lastErr = "failed to build request: " + err.Error()
			continue
		}
		httpReq.Header = c.hdr
		resp, err := client.Do(httpReq)
		if err != nil {
			lastErr = "request failed: " + err.Error()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body[:min(len(body), 512)]))
			continue
		}
		// Both OpenAI and Anthropic return {"data": [{"id": "..."},...]}.
		var parsed struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			lastErr = "failed to parse response: " + err.Error()
			continue
		}
		models := make([]string, 0, len(parsed.Data))
		for _, m := range parsed.Data {
			if m.ID != "" {
				models = append(models, m.ID)
			}
		}
		if len(models) > 0 {
			writeJSON(w, 200, map[string]any{"ok": true, "models": models})
			return
		}
		emptyOK = true // 200 but no model ids — keep trying other candidates
	}
	if emptyOK {
		writeJSON(w, 200, map[string]any{"ok": true, "models": []string{}})
		return
	}
	if lastErr == "" {
		lastErr = "no model list returned"
	}
	writeJSON(w, 200, map[string]any{"ok": false, "error": lastErr})
}
