//go:build ignore

package db

import "testing"

// A worker that finished normally but saved no checkpoint must not block completion;
// budget, timeout and partial exits still need an explicit stop decision.
func TestFinishedWithoutCheckpoint(t *testing.T) {
	node := func(state string, payload map[string]any) completionNode {
		return completionNode{Kind: KindIntent, State: state, Payload: payload}
	}
	cases := []struct {
		name string
		n    completionNode
		want bool
	}{
		{"finished, no checkpoint", node("exhausted", map[string]any{"execution_end": map[string]any{"requested_state": "done"}}), true},
		{"step limit", node("exhausted", map[string]any{"execution_end": map[string]any{"requested_state": "exhausted"}}), false},
		{"timeout wrap-up without end record", node("exhausted", map[string]any{}), false},
		{"partial checkpoint", node("exhausted", map[string]any{"checkpoint": map[string]any{"result": "partial"}, "execution_end": map[string]any{"requested_state": "done"}}), false},
		{"blocked intent", node("blocked", map[string]any{"execution_end": map[string]any{"requested_state": "done"}}), false},
	}
	for _, c := range cases {
		if got := finishedWithoutCheckpoint(c.n); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
