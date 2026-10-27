//go:build ignore

package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// Exercise the scheduler's actual relational query without requiring a running
// PostgreSQL server. Only the task/activity columns read by this query are needed.
func toolCallQueryDB(t *testing.T) *DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	d := &DB{DB: sqlDB}
	for _, query := range []string{
		`CREATE TABLE tasks (id INTEGER PRIMARY KEY, exploration_id INTEGER, description TEXT, goal TEXT, deleted_at TEXT)`,
		`CREATE TABLE activity (id INTEGER PRIMARY KEY, exploration_id INTEGER, node_id INTEGER, worker TEXT, kind TEXT, tool TEXT, tool_use_id TEXT, detail TEXT, is_error BOOLEAN, main_seg INTEGER)`,
		`INSERT INTO tasks(id, exploration_id, description, goal) VALUES (1, 10, 'multi-asset task', 'write reports')`,
	} {
		if _, err := d.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestNewToolCallsSincePairsEachResultOnce(t *testing.T) {
	t.Parallel()
	nodeA, nodeB := int64(101), int64(102)
	segZero, segOne := 0, 1
	tests := []struct {
		name string
		rows []Activity
		want map[string]string // result body -> the input for that particular call
	}{
		{
			name: "concurrent asset workers reuse call ID",
			rows: []Activity{
				{Worker: "worker-a", Kind: "tool_use", ToolUseID: "call_1", Detail: "asset-a"},
				{Worker: "worker-b", Kind: "tool_use", ToolUseID: "call_1", Detail: "asset-b"},
				{Worker: "worker-b", Kind: "tool_result", ToolUseID: "call_1", Detail: "finding-b"},
				{Worker: "worker-a", Kind: "tool_result", ToolUseID: "call_1", Detail: "finding-a"},
			},
			want: map[string]string{"finding-a": "asset-a", "finding-b": "asset-b"},
		},
		{
			name: "same worker reuses call ID in later turns",
			rows: []Activity{
				{Kind: "tool_use", ToolUseID: "call_1", Detail: "first-input"},
				{Kind: "tool_result", ToolUseID: "call_1", Detail: "first-finding"},
				{Kind: "tool_use", ToolUseID: "call_1", Detail: "second-input"},
				{Kind: "tool_result", ToolUseID: "call_1", Detail: "second-finding"},
			},
			want: map[string]string{"first-finding": "first-input", "second-finding": "second-input"},
		},
		{
			name: "same worker on separate intents",
			rows: []Activity{
				{NodeID: &nodeA, Kind: "tool_use", ToolUseID: "call_1", Detail: "intent-a"},
				{NodeID: &nodeB, Kind: "tool_use", ToolUseID: "call_1", Detail: "intent-b"},
				{NodeID: &nodeA, Kind: "tool_result", ToolUseID: "call_1", Detail: "finding-a"},
			},
			want: map[string]string{"finding-a": "intent-a"},
		},
		{
			name: "main agent session reset",
			rows: []Activity{
				{MainSeg: &segZero, Kind: "tool_use", ToolUseID: "call_1", Detail: "old-session"},
				{MainSeg: &segOne, Kind: "tool_use", ToolUseID: "call_1", Detail: "new-session"},
				{MainSeg: &segOne, Kind: "tool_result", ToolUseID: "call_1", Detail: "finding"},
			},
			want: map[string]string{"finding": "new-session"},
		},
		{
			name: "legacy null session is segment zero",
			rows: []Activity{
				{Kind: "tool_use", ToolUseID: "call_1", Detail: "legacy-input"},
				{MainSeg: &segZero, Kind: "tool_result", ToolUseID: "call_1", Detail: "finding"},
			},
			want: map[string]string{"finding": "legacy-input"},
		},
		{
			name: "other tool cannot supply finding input",
			rows: []Activity{
				{Kind: "tool_use", Tool: "Bash", ToolUseID: "call_1", Detail: "unrelated-command"},
				{Kind: "tool_result", ToolUseID: "call_1", Detail: "finding"},
			},
			want: map[string]string{"finding": ""},
		},
		{
			name: "missing call IDs must not pair unrelated events",
			rows: []Activity{
				{Kind: "tool_use", Detail: "unknown-input-a"},
				{Kind: "tool_use", Detail: "unknown-input-b"},
				{Kind: "tool_result", Detail: "finding"},
			},
			want: map[string]string{"finding": ""},
		},
		{
			name: "later input cannot pair with earlier result",
			rows: []Activity{
				{Kind: "tool_result", ToolUseID: "call_1", Detail: "finding"},
				{Kind: "tool_use", ToolUseID: "call_1", Detail: "future-input"},
			},
			want: map[string]string{"finding": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := toolCallQueryDB(t)
			var lastResultID int64
			for i, row := range tt.rows {
				if row.Tool == "" {
					row.Tool = "report_finding"
				}
				id := int64(i + 1)
				if _, err := d.Exec(`INSERT INTO activity(id, exploration_id, node_id, worker, kind, tool, tool_use_id, detail, is_error, main_seg) VALUES ($1,10,$2,$3,$4,$5,$6,$7,false,$8)`,
					id, row.NodeID, row.Worker, row.Kind, row.Tool, row.ToolUseID, row.Detail, row.MainSeg); err != nil {
					t.Fatal(err)
				}
				if row.Kind == "tool_result" {
					lastResultID = id
				}
			}
			events, err := d.NewToolCallsSince(0)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != len(tt.want) {
				t.Fatalf("got %d events for %d completed calls; each result must trigger once: %+v", len(events), len(tt.want), events)
			}
			seen := map[string]bool{}
			for _, event := range events {
				input, ok := tt.want[event.ToolOutput]
				if !ok || seen[event.ToolOutput] || event.ToolInput != input {
					t.Errorf("wrong input/result pairing: %+v; want input %q", event, input)
				}
				if event.TaskID != 1 || event.TaskDesc != "multi-asset task" || event.TaskGoal != "write reports" {
					t.Errorf("task context lost: %+v", event)
				}
				seen[event.ToolOutput] = true
			}
			remaining, err := d.NewToolCallsSince(lastResultID)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("watermark replayed completed calls: %+v, %v", remaining, err)
			}
		})
	}
}
