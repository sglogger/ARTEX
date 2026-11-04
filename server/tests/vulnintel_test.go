//go:build ignore

package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/vulnintel"
)

func vulnIntelTestServer(t *testing.T) *Server {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("postgres unavailable")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	keys := []string{settingVulnSources, vulnIntelSeedFlag, researcherSeedFlag}
	for _, src := range vulnintel.Sources {
		keys = append(keys, settingVulnKeyPrefix+src.ID)
	}
	cleanup := func() {
		for _, k := range keys {
			_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, k)
		}
		_, _ = pg.Exec(`DELETE FROM tools WHERE key IN ('vuln_lookup','cve_lookup','kev_search','ask_researcher')`)
		_, _ = pg.Exec(`DELETE FROM agents WHERE key=$1`, researcherAgentKey)
	}
	cleanup()
	t.Cleanup(cleanup)
	return &Server{m: &Manager{pg: pg, dir: t.TempDir()}}
}

func putVulnIntelJSON(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	s.putVulnIntel(w, httptest.NewRequest("PUT", "/api/settings/vuln-intel", strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func sourceRow(t *testing.T, payload map[string]any, id string) map[string]any {
	t.Helper()
	list, _ := payload["sources"].([]any)
	for _, raw := range list {
		row, _ := raw.(map[string]any)
		if row["id"] == id {
			return row
		}
	}
	t.Fatalf("source %s missing from %v", id, payload)
	return nil
}

func TestVulnIntelSettingsRoundTrip(t *testing.T) {
	s := vulnIntelTestServer(t)
	code, out := putVulnIntelJSON(t, s, `{"enabled":{"eu_kev":false},"keys":{"wordfence":"secret-key"}}`)
	if code != 200 {
		t.Fatalf("status %d %v", code, out)
	}
	if row := sourceRow(t, out, "eu_kev"); row["enabled"] != false {
		t.Errorf("eu_kev should be disabled: %v", row)
	}
	if row := sourceRow(t, out, "nvd"); row["enabled"] != true {
		t.Errorf("untouched sources default to enabled: %v", row)
	}
	wf := sourceRow(t, out, "wordfence")
	if wf["key_set"] != true {
		t.Errorf("wordfence key should be reported as set: %v", wf)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "secret-key") {
		t.Fatal("the API key must never be echoed back")
	}
	if cfg := s.vulnIntelSettings(); cfg.Keys["wordfence"] != "secret-key" || cfg.Enabled["eu_kev"] {
		t.Errorf("settings not applied: %+v", cfg)
	}

	// null keeps the stored key, "" clears it.
	if code, _ = putVulnIntelJSON(t, s, `{"keys":{"wordfence":null}}`); code != 200 {
		t.Fatalf("status %d", code)
	}
	if s.vulnIntelSettings().Keys["wordfence"] != "secret-key" {
		t.Error("null must leave the key untouched")
	}
	if _, out = putVulnIntelJSON(t, s, `{"keys":{"wordfence":""}}`); sourceRow(t, out, "wordfence")["key_set"] != false {
		t.Error(`"" must clear the key`)
	}
}

func TestVulnIntelSettingsRejectsUnknownInput(t *testing.T) {
	s := vulnIntelTestServer(t)
	if code, _ := putVulnIntelJSON(t, s, `{"enabled":{"shodan":true}}`); code != 400 {
		t.Errorf("unknown source accepted: %d", code)
	}
	if code, _ := putVulnIntelJSON(t, s, `{"keys":{"osv":"x"}}`); code != 400 {
		t.Errorf("key for a keyless source accepted: %d", code)
	}
}

func TestSeedVulnIntelToolsBindsDefaultsOnce(t *testing.T) {
	s := vulnIntelTestServer(t)
	s.seedVulnIntelTools()
	lookup, err := s.m.pg.GetTool("vuln_lookup")
	if err != nil || lookup == nil {
		t.Fatalf("vuln_lookup not seeded: %v", err)
	}
	for _, want := range []string{"worker", "planner", "mainagent", researcherAgentKey} {
		if !contains(lookup.Agents, want) {
			t.Errorf("vuln_lookup should be bound to %s: %v", want, lookup.Agents)
		}
	}
	ask, _ := s.m.pg.GetTool("ask_researcher")
	if ask == nil || contains(ask.Agents, "worker") || !contains(ask.Agents, "mainagent") || contains(ask.Agents, researcherAgentKey) {
		t.Errorf("ask_researcher bindings %v", ask)
	}
	// A manual unbind survives the next start.
	if err := s.m.pg.RemoveAgentFromTool("worker", "vuln_lookup"); err != nil {
		t.Fatal(err)
	}
	s.seedVulnIntelTools()
	if again, _ := s.m.pg.GetTool("vuln_lookup"); contains(again.Agents, "worker") {
		t.Error("seed re-bound a tool the user had unbound")
	}
}

func TestSeedResearcherAgent(t *testing.T) {
	s := vulnIntelTestServer(t)
	s.seedResearcherAgent()
	a, err := s.m.pg.GetAgentByKey(researcherAgentKey)
	if err != nil || a == nil {
		t.Fatalf("researcher not created: %v", err)
	}
	if !a.WebSearch || !a.Enabled {
		t.Errorf("researcher should have web search on: %+v", a)
	}
	// Runs once: a user who deletes the preset does not get it back.
	if err := s.m.pg.DeleteAgent(researcherAgentKey); err != nil {
		t.Fatal(err)
	}
	s.seedResearcherAgent()
	if again, _ := s.m.pg.GetAgentByKey(researcherAgentKey); again != nil {
		t.Error("deleted preset agent was recreated")
	}
}
