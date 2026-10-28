package agent

import actool "github.com/Autumn-27/norma/tool"

// This guidance is appended to the runtime prompt, including editable old prompts.
// Never reintroduce unbound tools or recreate a deleted Reporter.
func findingCaseWorkflow(key string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if key != "reporter" {
		return tools, ""
	}
	has := map[string]bool{}
	for _, t := range tools {
		has[t.Name()] = true
	}
	if !has["search_finding_duplicates"] || !has["merge_finding_records"] || !has["get_finding_case"] || !has["update_finding_case_report"] {
		return tools, ""
	}
	return tools, `

[Consolidating multiple reports of the same vulnerability] Keep this run's standalone report and don't overwrite other original reports. This extends the old instruction to stop as soon as the report is done: first save this report, then page through same-task candidate summaries with search_finding_duplicates(finding_id=standalone record ID), and view evidence with get_finding_record as needed. Similar vulnerability type/title or the same high/medium/low severity alone is no basis for merging; check the target, endpoint, parameters, permission boundary, root cause and actual verification evidence. When it is clearly the same defect, merge_finding_records(task_id,finding_ids,title,reason); when unsure, keep the standalone report and optionally mark a suspected duplicate with suggest_finding_merge for later review; never force a merge, and you must not wait for human confirmation and stop generating the report. Don't report again, don't delete records, don't re-probe the target. When a human selects records for consolidation, judge only the selected records and don't add unselected ones (existing case members can serve as report context).
When the merge succeeds or a case_id already exists, record the actual version from get_finding_case, page through all members, and read the original reports, PoCs and saved traffic with get_finding_record and get_finding_traffic as needed. update_finding_case_report saves the complete unified report plus a severity and severity_reason based on verified impact; you must not simply take the highest member severity. Present in one place the prerequisites, the complete execution order, how inputs and outputs connect, and complete runnable scripts and commands, without scattering required steps across the original reports; mark missing conditions and unverified steps truthfully. On a version conflict, re-read the material and regenerate; never just retry with a different version. Each member still keeps its standalone report, severity and evidence. When there is no clear duplicate, keep the standalone records and complete the report normally; a pending review suggestion is not a prerequisite for completing the report. When a tool is disabled, don't bypass the configuration.`
}
