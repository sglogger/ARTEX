// Package notify implements the IM / email push channel adapter layer for findings.
//
// Layering: this is a **leaf package** that depends only on the standard library. It knows nothing about the database or the server. Channel config
// comes in as map[string]any (matching the notification_channels.config JSONB column), and
// content to push comes in as a Message. The benefit of this split is that the genuinely error-prone parts, such as signature computation, UTF-8 truncation and filter matching,
// can be unit-tested without PostgreSQL, and the host only does orchestration on the server side.
//
// Concurrency contract: Channel implementations must be **stateless**. One Channel instance is shared concurrently by many channel configs
// (even by multiple robot instances of the same channel), all credentials come in via cfg,
// and caching things like the webhook URL in the implementation's own fields is not allowed.
package notify

// Channel kind identifiers. The values are also the valid set for notification_channels.kind, validated by a server-side
// allowlist (as with findings.status, no DB CHECK, which makes adding channels easier later).
const (
	KindDingTalk = "dingtalk" // DingTalk custom robot
	KindFeishu   = "feishu"   // Feishu (including Lark) custom robot
	KindWeCom    = "wecom"    // WeCom group robot
	KindWebhook  = "webhook"  // generic webhook: custom method/headers/JSON template
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // SMTP email
)

// Event kinds, matching notification_events.kind.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind is the fallback for an empty kind in config.
const InitKind = KindDingTalk

// severityRank maps finding severities to comparable ordinals. Unknown severities return 0, so any
// min_severity setting keeps unknown severities out: when in doubt, do not push, to avoid flooding with false positives.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank returns a severity's ordinal; unknown severities return 0.
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel returns the severity name with an emoji, used in message titles and card colors.
// Unknown severities are echoed as is rather than invented.
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 Critical"
	case "high":
		return "🟠 High"
	case "medium":
		return "🟡 Medium"
	case "low":
		return "🔵 Low"
	default:
		return severity
	}
}

// StatusLabel translates a disposition status into display text, used in status change messages.
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "Pending"
	case "in_progress":
		return "In progress"
	case "confirmed":
		return "Confirmed"
	case "resolved":
		return "Resolved"
	case "fixed":
		return "Fixed"
	case "false_positive":
		return "False positive"
	case "ignored":
		return "Ignored"
	case "duplicate":
		return "Duplicate"
	case "risk_accepted":
		return "Risk accepted"
	default:
		return status
	}
}

// AtLeast reports whether severity meets the min threshold. An empty min means no threshold, and everything passes.
// Note unknown severities have ordinal 0 and are rejected by any non-empty min (see the severityRank comment).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
