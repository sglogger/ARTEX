//go:build ignore

package server

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestSteerHooksRemindsMissingCheckpointOnce(t *testing.T) {
	final := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("WordPress 6.9.4 identified")}}}
	missing := true
	h := steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges,
		checkpointMissing: func() bool { return missing }, checkpointNudged: &atomic.Bool{}}
	if _, blocking, _ := h.Stop(context.Background(), final); len(blocking) != 1 || blocking[0] != checkpointNudge {
		t.Fatalf("first ending without checkpoint should be nudged: %v", blocking)
	}
	if _, blocking, _ := h.Stop(context.Background(), final); blocking != nil {
		t.Fatalf("the reminder must fire only once: %v", blocking)
	}

	missing = false
	saved := steerHooks{checkpointMissing: func() bool { return missing }, checkpointNudged: &atomic.Bool{}}
	if _, blocking, _ := saved.Stop(context.Background(), final); blocking != nil {
		t.Fatalf("no reminder once a checkpoint exists: %v", blocking)
	}
	// An inner hard stop wins over the reminder.
	inner := steerHooks{inner: fakeHooks{prevent: true}, checkpointMissing: func() bool { return true }, checkpointNudged: &atomic.Bool{}}
	if prevent, blocking, _ := inner.Stop(context.Background(), final); !prevent || blocking != nil {
		t.Fatalf("inner stop not respected: prevent=%v blocking=%v", prevent, blocking)
	}
}

func TestIntentNeedsCheckpoint(t *testing.T) {
	cases := map[string]bool{
		`{"summary":"x"}`:                                        false,
		`{"check_key":"abc"}`:                                    true,
		`{"check_key":"abc","checkpoint":null}`:                  true,
		`{"check_key":"abc","checkpoint":{"result":"negative"}}`: false,
	}
	for raw, want := range cases {
		if got := intentNeedsCheckpoint(json.RawMessage(raw)); got != want {
			t.Errorf("%s: got %v want %v", raw, got, want)
		}
	}
}

func TestIntentExitReason(t *testing.T) {
	cases := map[string]string{
		`{"check_key":"a","execution_end":{"state":"exhausted","requested_state":"done"}}`: "no_checkpoint",
		`{"checkpoint":{"result":"partial"},"execution_end":{"requested_state":"done"}}`:   "partial",
		`{"checkpoint":{"result":"blocked"}}`:                                              "partial",
		`{"execution_end":{"state":"exhausted","requested_state":"exhausted"}}`:            "budget",
		`{"summary":"timed out during task wrap-up"}`:                                      "budget",
	}
	for raw, want := range cases {
		if got := intentExitReason(json.RawMessage(raw)); got != want {
			t.Errorf("%s: got %q want %q", raw, got, want)
		}
	}
}
