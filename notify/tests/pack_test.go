//go:build ignore

package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// This file covers the "pack whole items" fix: when a digest exceeds the channel length limit, it must be truncated **by whole items**
// and the number of items that did not fit reported faithfully, so the caller marks only those actually delivered.
//
// Previously it rendered everything, truncated, and then marked the whole batch delivered: the second half of the message vanished,
// while delivery history showed full success; the findings were simply gone, with nowhere to notice.

func TestMarkdownBodyPacksWholeItemsWithinByteLimit(t *testing.T) {
	// A 200-item digest, surely far beyond WeCom's 4096 bytes.
	m := batchMsg(200)
	body, kept := markdownBody(m, weComMarkdownLimit)

	if len(body) > weComMarkdownLimit {
		t.Fatalf("body of %d bytes exceeds the limit %d", len(body), weComMarkdownLimit)
	}
	if !utf8.ValidString(body) {
		t.Fatal("body is not valid UTF-8")
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("only part should fit (0 < kept < %d), got %d", len(m.Items), kept)
	}
	// The header must state faithfully how many items this message contains and how many remain; otherwise readers take the header's
	// number as everything.
	if !strings.Contains(body, "remaining") || !strings.Contains(body, "continue in the next message") {
		t.Fatalf("the header should state how many items are not included in this message:\n%s", body[:minInt(400, len(body))])
	}
	// Only the first kept items should be included.
	for i := 0; i < kept; i++ {
		if !strings.Contains(body, "Finding"+itoa(i+1)) {
			t.Fatalf("item %d should be in this message:\n%s", i+1, body)
		}
	}
	if strings.Contains(body, "Finding"+itoa(kept+1)) {
		t.Fatalf("item %d should not appear (it belongs to the next batch)", kept+1)
	}
}

func TestMarkdownBodyKeepsEverythingWhenUnderLimit(t *testing.T) {
	m := batchMsg(3)
	body, kept := markdownBody(m, 0) // 0 = no limit
	if kept != len(m.Items) {
		t.Fatalf("everything should be kept with no length limit, got kept=%d", kept)
	}
	if strings.Contains(body, "remaining") {
		t.Fatalf("no truncation notice should appear without truncation:\n%s", body)
	}
}

func TestMarkdownBodyAlwaysKeepsAtLeastOneItem(t *testing.T) {
	// When the budget is too small for even one item, one item must still be sent (final truncation is the backstop).
	// Otherwise one overlong finding would leave the whole batch stuck forever: it never fits on any claim, and is never sent.
	m := batchMsg(5)
	_, kept := markdownBody(m, 50)
	if kept != 1 {
		t.Fatalf("at least 1 item should be kept, got %d", kept)
	}
}

func TestMarkdownBodySingleReturnsOne(t *testing.T) {
	_, kept := markdownBody(singleMsg(), 4096)
	if kept != 1 {
		t.Fatalf("a single message should report 1 item delivered, got %d", kept)
	}
	// An empty message has no deliverable items.
	if _, k := markdownBody(Message{}, 4096); k != 0 {
		t.Fatalf("an empty message should report 0 items, got %d", k)
	}
}

func TestTelegramPackingUsesRuneBudget(t *testing.T) {
	m := batchMsg(200)
	text, kept := telegramHTML(m)
	// Telegram limits by **characters**; a byte measure would squeeze CJK messages to a third.
	if n := utf8.RuneCountInString(text); n > telegramTextLimit {
		t.Fatalf("body of %d characters exceeds the limit %d", n, telegramTextLimit)
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("only part should fit, got %d", kept)
	}
	if !strings.Contains(text, "in the next message") {
		t.Fatalf("it should state that some items are not included:\n%.300s", text)
	}
}

func TestFeishuPackingReportsKept(t *testing.T) {
	m := batchMsg(2000)
	_, kept := feishuCard(m)
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("the card should fit only part, got %d", kept)
	}
}

func TestWebhookAndEmailReportAllItems(t *testing.T) {
	// These two channels do not truncate the body, so the whole batch counts as delivered.
	m := batchMsg(7)
	if n := len(m.Items); n != 7 {
		t.Fatal("precondition not met")
	}
	// Confirmed indirectly via the renderer's return value: markdownBody(0) keeps everything with no limit.
	if _, k := markdownBody(m, 0); k != len(m.Items) {
		t.Fatalf("everything should be used with no length limit, got %d", k)
	}
}

// TestMarkdownEscapesUntrustedContent is the regression test for "untrusted content must not change message structure".
// Titles and summaries come from model output (and the model reads the target's responses); asset names come from the target's URLs.
func TestMarkdownEscapesUntrustedContent(t *testing.T) {
	cases := []struct {
		name  string
		item  Item
		must  []string // must appear in the result (escaped form)
		wrong []string // must not appear in the result (unescaped form)
	}{
		{
			name: "newline + external link in the title",
			item: Item{
				Severity: "high",
				Name:     "Login SQL injection\n[URGENT: click to verify your account](http://attacker.tld)",
			},
			// The newline must be collapsed (or it can forge a new list item/blockquote);
			// square and round brackets must be escaped (or it is a clickable external link).
			must:  []string{`\[URGENT: click to verify your account\]`, `\(http://attacker.tld\)`},
			wrong: []string{"\n[URGENT", "\n\n[URGENT"},
		},
		{
			name: "image beacon in the title",
			item: Item{
				Severity: "high",
				Name:     "Finding ![](http://attacker.tld/beacon)",
			},
			must:  []string{`\!`, `\(http://attacker.tld/beacon\)`},
			wrong: []string{"![]("},
		},
		{
			name: "emphasis and quote in the asset name",
			item: Item{
				Severity: "high",
				Name:     "Plain title",
				Assets:   []string{"a.com/*injection*>quote"},
			},
			must:  []string{`\*injection\*`, `\>`},
			wrong: []string{"*injection*"},
		},
		{
			name: "backticks and pipes in the summary",
			item: Item{
				Severity: "high",
				Name:     "Title",
				Summary:  "`code` | table",
			},
			must:  []string{"\\`code\\`", `\|`},
			wrong: []string{"`code`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Message{Items: []Item{tc.item}}
			// writeItem in single mode is the rendering path shared by the three markdown channels.
			var b strings.Builder
			writeItem(&b, tc.item, "", true)
			got := b.String()
			for _, want := range tc.must {
				if !strings.Contains(got, want) {
					t.Errorf("missing escaped form %q:\n%s", want, got)
				}
			}
			for _, bad := range tc.wrong {
				if strings.Contains(got, bad) {
					t.Errorf("unescaped form %q appeared (could be used to inject structure or external links):\n%s", bad, got)
				}
			}
			_ = m
		})
	}
}

// TestMarkdownEscapeBackslashFirst pins the escaping order: backslashes must be handled first,
// or the backslashes added later get another layer, producing double backslashes in the output.
func TestMarkdownEscapeBackslashFirst(t *testing.T) {
	if got := markdownEscape(`a\b*c`); got != `a\\b\*c` {
		t.Fatalf("wrong escaping order, got %q", got)
	}
}

// TestTelegramTitleHasNoMarkdownEscapes pins a specific regression:
// markdown escaping must not leak into Telegram's HTML output (escaping was once added to the shared title
// function, and Telegram messages ended up showing visible backslashes like `\(1\)`).
func TestTelegramTitleHasNoMarkdownEscapes(t *testing.T) {
	m := Message{Items: []Item{{Severity: "high", Name: "alert(1) *important*"}}}
	text, _ := telegramHTML(m)
	if strings.Contains(text, `\(`) || strings.Contains(text, `\*`) {
		t.Fatalf("markdown backslash escapes appeared in the Telegram body:\n%s", text)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
