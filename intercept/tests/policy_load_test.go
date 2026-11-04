//go:build ignore

package intercept

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	_ "modernc.org/sqlite"
)

func policyDB(t *testing.T) *db.DB {
	t.Helper()
	pool, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	d := &db.DB{DB: pool}
	policyExec(t, d, `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`)
	policyExec(t, d, `CREATE TABLE intercept_rules (
		id INTEGER PRIMARY KEY, name TEXT, enabled BOOLEAN, priority INTEGER,
		match_target TEXT, match_type TEXT, pattern TEXT, action TEXT, message TEXT,
		timeout_enabled BOOLEAN, timeout_seconds INTEGER, timeout_action TEXT,
		created_at DATETIME, updated_at DATETIME)`)
	return d
}

func policyExec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func policyRule(t *testing.T, d *db.DB, matchType, pattern string) {
	t.Helper()
	policyExec(t, d, `INSERT INTO intercept_rules VALUES
		(1, 'deny probe', true, 100, 'tool_name', ?, ?, 'deny', 'policy denies Bash',
		false, 0, 'deny', ?, ?)`, matchType, pattern, time.Now(), time.Now())
}

func TestPolicyLoadErrorsAreNotDisabledOrNonMatches(t *testing.T) {
	d := policyDB(t)
	policyRule(t, d, "contains", "Bash")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	ic := New(d)
	if _, err := ic.IsToolEnabledWithError("Bash"); err == nil {
		t.Fatal("closed database reported an explicitly disabled tool")
	}
	if _, _, err := ic.MatchWithError("Bash", []byte(`{}`)); err == nil {
		t.Fatal("closed database reported a successful non-match")
	}
	// Existing callers using the compatibility methods must also fail closed.
	if !ic.IsToolEnabled("Bash") {
		t.Fatal("compatibility method bypassed gating on a load failure")
	}
	if dec, matched := ic.Match("Bash", []byte(`{}`)); !matched || dec.Action != "deny" || dec.Message == "" {
		t.Fatalf("compatibility method failed open: %+v %v", dec, matched)
	}
}

func TestPolicySnapshotSurvivesUntilInvalidated(t *testing.T) {
	d := policyDB(t)
	policyRule(t, d, "contains", "Bash")
	ic := New(d)
	if dec, matched, err := ic.MatchWithError("Bash", []byte(`{}`)); err != nil || !matched || dec.Action != "deny" {
		t.Fatalf("initial policy missing: %+v %v %v", dec, matched, err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if enabled, err := ic.IsToolEnabledWithError("Bash"); err != nil || !enabled {
		t.Fatalf("complete tool snapshot discarded: %v %v", enabled, err)
	}
	if dec, matched, err := ic.MatchWithError("Bash", []byte(`{}`)); err != nil || !matched || dec.Action != "deny" {
		t.Fatalf("complete rule snapshot discarded: %+v %v %v", dec, matched, err)
	}
	ic.Invalidate()
	if _, _, err := ic.MatchWithError("Bash", []byte(`{}`)); err == nil {
		t.Fatal("invalidation hid the subsequent load failure")
	}
}

func TestEmptyRulesAreACompletePolicySnapshot(t *testing.T) {
	d := policyDB(t)
	ic := New(d)
	if _, matched, err := ic.MatchWithError("Bash", []byte(`{}`)); err != nil || matched {
		t.Fatalf("empty policy should successfully match nothing: %v %v", matched, err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, matched, err := ic.MatchWithError("Bash", []byte(`{}`)); err != nil || matched {
		t.Fatalf("empty loaded policy was unnecessarily reloaded: %v %v", matched, err)
	}
}

func TestConfigurationLoadCannotPublishPartialPolicy(t *testing.T) {
	for _, broken := range []string{"missing settings table", "invalid tool JSON"} {
		t.Run(broken, func(t *testing.T) {
			d := policyDB(t)
			policyRule(t, d, "contains", "Bash")
			if broken == "missing settings table" {
				policyExec(t, d, `DROP TABLE settings`)
			} else {
				policyExec(t, d, `INSERT INTO settings VALUES ('intercept_enabled_tools', '{broken')`)
			}
			ic := New(d)
			for range 2 {
				if _, _, err := ic.MatchWithError("Bash", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "configuration") {
					t.Fatalf("partial rule cache hid a configuration failure: %v", err)
				}
			}
			if broken == "missing settings table" {
				policyExec(t, d, `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`)
			} else {
				policyExec(t, d, `UPDATE settings SET value='["Bash"]' WHERE key='intercept_enabled_tools'`)
			}
			if dec, matched, err := ic.MatchWithError("Bash", []byte(`{}`)); err != nil || !matched || dec.Action != "deny" {
				t.Fatalf("failed load did not recover after repair: %+v %v %v", dec, matched, err)
			}
		})
	}
}

func TestInvalidRegexDoesNotSilentlyRemoveDenyRule(t *testing.T) {
	d := policyDB(t)
	policyRule(t, d, "regex", "[")
	if _, _, err := New(d).MatchWithError("Bash", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "compile intercept rule 1") {
		t.Fatalf("invalid deny regex disappeared without an error: %v", err)
	}
}

func TestExplicitEmptyToolsRemainDisabled(t *testing.T) {
	d := policyDB(t)
	policyExec(t, d, `INSERT INTO settings VALUES ('intercept_enabled_tools', '[]')`)
	enabled, err := New(d).IsToolEnabledWithError("Bash")
	if err != nil || enabled {
		t.Fatalf("explicit empty tools must remain a valid disabled configuration: %v %v", enabled, err)
	}
}

func TestJudgeConfigurationReadFailureDenies(t *testing.T) {
	d := policyDB(t)
	ic := New(d)
	if _, err := ic.GetJudgeConfigWithError(); err != nil {
		t.Fatal(err)
	}
	policyExec(t, d, `DROP TABLE settings`)
	if _, err := ic.GetJudgeConfigWithError(); err == nil {
		t.Fatal("judge configuration swallowed a settings read error")
	}
	if dec, judged := ic.Judge(t.Context(), "Bash", []byte(`{}`)); !judged || dec.Action != "deny" || !strings.Contains(dec.Message, "llm_judge_enabled") {
		t.Fatalf("unreadable judge configuration disabled fallback: %+v %v", dec, judged)
	}
}

// A judge outage must not approve calls: every attempt is retried, and only after
// the last failure the failure policy applies, which defaults to a human decision.
func TestJudgeRetriesThenEscalatesToHuman(t *testing.T) {
	d := policyDB(t)
	policyExec(t, d, `INSERT INTO settings VALUES ('llm_judge_enabled', 'true')`)
	ic := New(d)
	calls := 0
	ic.SetReviewer(func(context.Context, int64, string, ReviewInput) (Decision, error) {
		calls++
		return Decision{}, errors.New("context deadline exceeded")
	})
	dec, judged := ic.Judge(t.Context(), "Bash", []byte(`{"command":"id"}`))
	if !judged || dec.Action != "ask" || !dec.ModelFallback || calls != judgeAttempts || !strings.Contains(dec.Message, "after 3 attempts") {
		t.Fatalf("judge outage: %+v judged=%v calls=%d", dec, judged, calls)
	}
}

func TestJudgeRetryAcceptsLaterValidVerdict(t *testing.T) {
	d := policyDB(t)
	policyExec(t, d, `INSERT INTO settings VALUES ('llm_judge_enabled', 'true')`)
	ic := New(d)
	calls := 0
	ic.SetReviewer(func(context.Context, int64, string, ReviewInput) (Decision, error) {
		calls++
		if calls == 1 {
			return Decision{Action: "maybe"}, nil // invalid verdict counts as a failure
		}
		return Decision{Action: "deny", Message: "destructive"}, nil
	})
	dec, judged := ic.Judge(t.Context(), "Bash", []byte(`{"command":"rm -rf /"}`))
	if !judged || dec.Action != "deny" || dec.ModelFallback || calls != 2 {
		t.Fatalf("retry verdict: %+v judged=%v calls=%d", dec, judged, calls)
	}
}

func TestJudgeExplicitAllowFailurePolicyIsKept(t *testing.T) {
	d := policyDB(t)
	policyExec(t, d, `INSERT INTO settings VALUES ('llm_judge_enabled', 'true')`)
	policyExec(t, d, `INSERT INTO settings VALUES ('llm_judge_fail_action', 'allow')`)
	ic := New(d)
	ic.SetReviewer(func(context.Context, int64, string, ReviewInput) (Decision, error) {
		return Decision{}, errors.New("down")
	})
	if dec, judged := ic.Judge(t.Context(), "Bash", []byte(`{"command":"id"}`)); !judged || dec.Action != "allow" {
		t.Fatalf("explicit allow policy not applied: %+v %v", dec, judged)
	}
}
