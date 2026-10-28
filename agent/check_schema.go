package agent

import (
	"encoding/json"
	actool "github.com/Autumn-27/norma/tool"
)

// Merge additive contract fields after catalog overrides, without replacing the
// operator's descriptions/defaults or re-enabling disabled tools.
func withCheckContractSchema(t actool.CoreTool) actool.CoreTool {
	var additions map[string]any
	batch := ""
	switch t.Name() {
	case "set_goals":
		additions = map[string]any{"goal_type": map[string]any{"type": "string", "enum": []string{"inspection", "outcome"}}, "required_checks": map[string]any{"type": "array", "items": str("required objects, mechanisms and completion conditions")}}
		batch = "goals"
	case "add_intent":
		additions = map[string]any{"check": obj(map[string]any{"target": str("normalized target/endpoint"), "mechanism": str("fixed check mechanism")}, "target", "mechanism"), "retry_of": idp("original intent being recovered"), "recovery_change": str("what actually changes")}
		batch = "intents"
	case "record_fact":
		additions = map[string]any{"checkpoint": checkpointSchema()}
		batch = "facts"
	default:
		return t
	}
	raw, err := json.Marshal(t.InputSchema())
	if err != nil {
		return t
	}
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil {
		return t
	}
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
		schema["properties"] = properties
	}
	for k, v := range additions {
		properties[k] = v
	}
	if field, ok := properties[batch].(map[string]any); ok {
		itemSchema, _ := field["items"].(map[string]any)
		if itemSchema == nil {
			itemSchema = map[string]any{"type": "object"}
			field["items"] = itemSchema
		}
		items, _ := itemSchema["properties"].(map[string]any)
		if items == nil {
			items = map[string]any{}
			itemSchema["properties"] = items
		}
		for k, v := range properties {
			if k != batch && items[k] == nil {
				items[k] = v
			}
		}
		for k, v := range additions {
			items[k] = v
		}
		if t.Name() == "set_goals" {
			raw, _ := json.Marshal(itemSchema["required"])
			var required []string
			_ = json.Unmarshal(raw, &required)
			found := false
			for _, name := range required {
				found = found || name == "goal_type"
			}
			if !found {
				required = append(required, "goal_type")
			}
			itemSchema["required"] = required
		}
	}
	note := "\nSave the check identity by fixed target/mechanism; a recovery references the latest retry_of and explains recovery_change."
	if t.Name() == "set_goals" {
		note = "\nSave a fixed goal_type and required_checks; inspection can be satisfied by a complete negative check, outcome requires result evidence."
	} else if t.Name() == "record_fact" {
		note = "\ncheckpoint must truthfully record coverage, results and gaps; resuming keeps the original authentication candidates, budget and verified identity."
	}
	return DecorateTool(t, t.Description()+note, schema)
}

func checkpointSchema() map[string]any {
	stringsSchema := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return obj(map[string]any{
		"result": map[string]any{"type": "string", "enum": []string{"positive", "negative", "partial", "blocked"}},
		"scope":  stringsSchema, "evidence": str("the actual operation and verifiable result"), "gaps": stringsSchema,
		"barrier": str("the specific obstacle"), "next_action": str("what recovery must change"),
		"auth": obj(map[string]any{
			"request": str("real endpoint, method, Content-Type and field names; no credentials"),
			"budget":  intp("total number of authorized candidate attempts"),
			"candidates": map[string]any{"type": "array", "items": obj(map[string]any{
				"id": str("fixed candidate reference, no password"), "state": map[string]any{"type": "string", "enum": []string{"remaining", "failed", "skipped", "verified"}}, "reason": str("skip/stop reason"), "attempts": intp("number of requests actually sent, including those interrupted by errors/rate limiting; must not decrease on resume"),
			}, "id", "state")},
			"identity": str("verified identity and privileges"), "identity_evidence": str("post-authentication low-impact read and key response"), "stop_reason": str("stop reason such as rate limiting/lockout"),
		}, "request", "budget", "candidates"),
	}, "result", "scope", "evidence", "gaps")
}
