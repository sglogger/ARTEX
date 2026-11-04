//go:build ignore

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// An intent id passed as asset id used to surface as a raw foreign-key error.
func TestAddIntentRejectsUnknownAssetIDs(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTaskWithOptions("asset id check", "target: https://example.test", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	ts.SetTaskID(task.ID)
	ts.SetAssetStore(d.Assets(), nil)

	missing := int64(9_000_000_000)
	in, _ := json.Marshal(map[string]any{"intents": []map[string]any{
		{"summary": "probe endpoints", "asset_ids": []int64{missing}},
		{"summary": "fingerprint the site", "asset_ids": []int64{0}},
	}})
	res, err := ts.addIntent().Call(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		IDs    []int64           `json:"ids"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(res.Flatten()), &out); err != nil {
		t.Fatalf("decode %s: %v", res.Flatten(), err)
	}
	if out.IDs[0] != 0 || !strings.Contains(out.Errors["0"], "not assets") || strings.Contains(out.Errors["0"], "SQLSTATE") {
		t.Errorf("unknown asset id should be rejected with a readable error: %+v", out)
	}
	if out.IDs[1] == 0 {
		t.Errorf("asset id 0 means no asset and must still create the intent: %+v", out)
	}
}
