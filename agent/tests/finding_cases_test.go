//go:build ignore

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestFindingCaseGuidanceRespectsToolsAndTrafficSwitch(t *testing.T) {
	tools := []actool.CoreTool{}
	for _, name := range []string{"search_finding_duplicates", "merge_finding_records", "get_finding_case", "update_finding_case_report"} {
		tools = append(tools, writeTool(name, name, obj(nil), func(context.Context, json.RawMessage) (actool.Result, error) { return actool.Text("ok"), nil }))
	}
	_, guide := findingCaseWorkflow("reporter", tools)
	if !strings.Contains(guide, "must not simply take the highest member severity") || !strings.Contains(guide, "standalone report") {
		t.Fatal("missing preservation rules")
	}
	if _, guide := findingCaseWorkflow("worker", tools); guide != "" {
		t.Fatal("worker changed")
	}
	if _, guide := findingCaseWorkflow("reporter", tools[:2]); guide != "" {
		t.Fatal("unbound tools reintroduced")
	}
	system, _ := deferredSystem("CUSTOM PROMPT", DeferredInfo{CaseGuidance: guide})
	if !strings.HasPrefix(system[0], "CUSTOM PROMPT") || !strings.Contains(system[0], guide) {
		t.Fatal("custom prompt lost")
	}
}

func TestFindingCaseWorkflowDoesNotWaitForApproval(t *testing.T) {
	tools := []actool.CoreTool{}
	for _, name := range []string{"search_finding_duplicates", "merge_finding_records", "get_finding_case", "update_finding_case_report"} {
		tools = append(tools, writeTool(name, name, obj(nil), func(context.Context, json.RawMessage) (actool.Result, error) { return actool.Text("ok"), nil }))
	}
	_, guidance := findingCaseWorkflow("reporter", tools)
	if !strings.Contains(guidance, "must not wait for human confirmation") || !strings.Contains(guidance, "not a prerequisite for completing the report") {
		t.Fatal("workflow must not gate report generation on human approval")
	}
}
