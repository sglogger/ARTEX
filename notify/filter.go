package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter is the contract for the notification_channels.filter JSONB column: a channel instance's filter conditions.
// All fields are optional and default to "no filtering", which is exactly the fallback semantics for malformed config; see ParseFilter.
type Filter struct {
	// MinSeverity is the minimum severity threshold (low/medium/high/critical); empty means no threshold.
	MinSeverity string `json:"min_severity"`
	// Empty TaskIDs / AssetIDs mean unrestricted; non-empty requires the event to intersect them.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// Empty VulnClassInclude accepts everything; non-empty requires vulnclass to match any keyword.
	// Matching any VulnClassExclude keyword excludes the event (exclusion wins over inclusion).
	// Matching is case-insensitive substring, which is safer than regex: a misconfigured regex cannot silently disable the channel.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange decides whether the channel receives finding status change events (meaningful only in realtime mode).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter parses a channel filter config.
//
// **It never returns an error.** This is a deliberate design choice: malformed filter config always degrades to the zero
// Filter (= no filtering = everything matches), because for a finding notification system **one extra push is far better than
// silently missing a high-severity finding**. Turning a parse failure into "no pushes" would give users a channel that looks configured
// but actually pushes nothing, which is the worst failure mode.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// On parse failure f stays zero, i.e. no filtering.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity reports whether s is a valid severity threshold (empty string means no threshold).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate checks the **value-restricted** fields of a filter config, called when a channel is saved.
//
// Why this must be caught on write: Match treats an unknown threshold as `rank >= 0`, which is always true,
// so a typo in min_severity ("hgih") makes the filter **silently ineffective** and
// "push everything". That matches this package's "over-push rather than miss" direction (nothing is missed),
// but the user believes they set up severity-based pushes while every finding floods the group,
// with no sign that anything is misconfigured. This kind of silent degradation should be stopped at the entry point.
//
// Note Validate is used only on the **write** path. The read path still uses ParseFilter's lenient semantics,
// so bad values already in historical data do not make the whole channel unreadable.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("invalid minimum severity %q; choose low / medium / high / critical, or leave empty for no limit", f.MinSeverity)
	}
	return nil
}

// Match decides whether an event should be delivered to a channel with this filter.
//
// **It never returns an error**, for the same reason as ParseFilter: any internal anomaly counts as a match.
// Evaluation order: event kind → severity threshold → task/asset scope → vulnclass keywords.
func Match(f Filter, s Snapshot) bool {
	// Status change events are received only by channels that explicitly opt in. Off by default, because most users
	// expect a "push" to mean "a new finding was discovered", not a running log of every status transition.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// Exclusion wins: matching any exclude keyword rules the event out, even if it also matches the include list.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// A linear scan suffices for small sets; both sides are on the order of "a few dozen hand-picked items",
	// so building a map costs more than it saves.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold reports whether s contains any of keywords (case-insensitive).
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
