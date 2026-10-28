package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

func (t *ToolSet) listRemainingWork() actool.CoreTool {
	return t.readExpTool("list_remaining_work",
		"Page through every non-done intent of this task, including open/running/paused/blocked/exhausted/stopped/deleted, returning the total and the stop/blocked reasons. Before wrapping up you must check page by page: when has_more=true use next_after as the next after, until has_more=false. Old intents won't be missed because of overview truncation; required work must still be completed, and only duplicate or irrelevant work may be stopped by submitting remaining_work stop reasons.",
		obj(map[string]any{"after": intp("next_after of the previous page; omit or 0 for the first page"), "limit": intp("items per page, default 100, max 200")}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var args struct {
				After int64 `json:"after"`
				Limit int   `json:"limit"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return actool.Errorf("invalid list_remaining_work arguments: " + err.Error()), nil
			}
			page, err := t.ts.ListRemainingWork(ctx, args.After, args.Limit)
			if err != nil {
				return actool.Errorf("list_remaining_work: " + err.Error()), nil
			}
			return jsonResult(page)
		})
}
