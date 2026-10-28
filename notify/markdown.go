package notify

import (
	"fmt"
	"strings"
)

// This file is the shared message rendering for "Markdown family" channels (DingTalk, WeCom).
// Feishu uses card JSON, Telegram HTML and email HTML, each rendered in its own adapter.

// maxAssetsShown is the maximum number of assets listed in a message. A finding may be anchored to dozens of assets,
// and listing them all would bloat the message with no informational value; nobody reads the 4th domain onward in IM.
const maxAssetsShown = 3

// maxSummaryRunes is how many characters the summary is compressed to. An IM message is a prompt to go look at the details,
// not the report itself; the full content is in the platform.
const maxSummaryRunes = 120

// markdownReservedBytes is reserved for the message header (digest line + severity breakdown + possible truncation notice)
// and footer (platform link). It is subtracted from the budget when packing whole items, so the header and footer are never cut off;
// once they are cut, readers cannot even tell which batch this is or how many items were not shown.
const markdownReservedBytes = 320

// markdownEscape escapes markdown metacharacters.
//
// Why this is required: finding titles, summaries, types and asset display names all come from **untrusted sources**:
// titles and summaries come from model output (and the model reads the target's responses), and asset urls are full
// URLs obtained by scanning (including target-controlled query strings). Without escaping, a finding titled
//
//	Login SQL injection\n[URGENT: click to verify your account](http://attacker.tld)
//
// would render as a **clickable external link** in a security engineer's DingTalk/Feishu; and
// `![](http://attacker.tld/beacon)` would be fetched by the client at render time, announcing
// "this finding has been viewed" and leaking the reader's IP. Even with benign content, injected bold text or
// blockquotes can push critical findings below the fold.
//
// The escape set covers characters for headings/links/emphasis/lists/quotes/strikethrough that change structure or create clickable
// elements. `\` must be handled first, or the backslashes added later would be escaped again.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText compresses untrusted text to a single line and escapes it, for use in markdown bodies.
// Single-lining is the other half beyond escaping: a newline alone can forge a new list item or blockquote,
// which escape characters cannot stop.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle returns the message title (IM platform title bar/card title) as **unescaped raw text**.
//
// It is deliberately not escaped: this title is shared by renderers in four contexts: markdown bodies, Telegram
// HTML, Feishu card plain_text, and the generic webhook JSON and email subject. Each context has
// different escaping rules (markdown escaping inside HTML leaves visible backslashes, inside JSON it pollutes
// the data), so escaping must be done by each output, see writeItem / feishuItemLines /
// telegramEscape. Markdown escaping was once added to the shared function, and Telegram messages ended up
// showing visible backslashes like `\(1\)`.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("Finding digest · %d total", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "Finding notification"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody renders the message body and returns it with the **number of items actually written**.
//
// The returned kept is the number of items actually delivered by this send; the caller uses it to mark only the first kept items as
// delivered. Items kept out by the channel length limit must wait for the next batch instead of being
// marked successful along with the rest. This is exactly where "silent loss" comes from: the message was truncated, but the delivery record shows everything delivered,
// and nowhere reveals that the second half was never sent.
//
// maxBytes<=0 means no limit.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// A single message is sent even if too long (the final truncation is the backstop): partial information about one finding
		// beats sending nothing.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\n[View all in platform](%s)\n", m.HomeURL)
	}
	kept := packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items := m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it := range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro renders the start of a digest message: time window, count and severity breakdown.
// With these, recipients can judge whether the batch needs immediate attention without opening the platform.
//
// items are the items that **actually fit**, total is the batch's full count. When they differ it must say explicitly
// how many more are in the next message; otherwise readers assume the number in the header is everything,
// and the items that were never sent simply do not exist in the UI.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**%[2]d new findings in the last %[1]d minutes**", m.WindowMinutes, total)
	} else {
		fmt.Fprintf(&b, "**%d new findings**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, " (showing the first %d here; the remaining %d continue in the next message)", len(items), extra)
	}
	// Give the per-severity breakdown so readers see at a glance whether anything is critical. Count only the items **actually included in this message**,
	// so "Critical 3" matches the items that can be counted below.
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem renders one finding item.
//
// prefix is the sequence number in digest lists; single=true renders the full version (with summary and backlink),
// while digest lists render only a one-line summary; otherwise a 50-item digest would turn into a long document.
//
// All externally sourced content (title/type/assets/summary) goes through markdownText:
// single-line + escaping. The backlink is built from the admin-configured public_base_url, is not untrusted content,
// and must be a clickable link, so it is output as is.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// Digest mode: rendered on one line, with compressed assets and summary following.
		var extras []string
		if a := assetLine(it.Assets, maxAssetsShown); a != "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary != "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**Status change**: %s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**Type**: %s\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**Assets**: %s\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**Summary**: %s\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[View details](%s)\n", it.DetailURL)
	}
}
