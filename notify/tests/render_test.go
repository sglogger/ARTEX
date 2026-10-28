//go:build ignore

package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// This is the package's most important invariant. WeCom limits by **bytes**, with 3 bytes per CJK character,
	// so any implementation that cuts hard by bytes splits characters in half, producing invalid UTF-8 that the platform rejects.
	// Hit every possible cut point with several multi-byte and ASCII mixed inputs of coprime lengths.
	inputs := []string{
		"Ünïcödé tëst cöntënt",
		"mixed ü€ content ✓",
		"a€b✓c★d⚠e",
		"🔴🟠🟡🔵", // 4-byte emoji make a wrong cut more obvious
		strings.Repeat("€✓", 100),
	}
	for _, in := range inputs {
		for max := 1; max <= len(in)+2; max++ {
			got := TruncateBytes(in, max)
			if !utf8.ValidString(got) {
				t.Fatalf("input %q max=%d: produced invalid UTF-8 %q", in, max, got)
			}
			if len(got) > max {
				t.Fatalf("input %q max=%d: result of %d bytes exceeds the limit", in, max, len(got))
			}
			// Content must not change when it was not truncated.
			if len(in) <= max && got != in {
				t.Fatalf("input %q max=%d: content changed without exceeding the limit -> %q", in, max, got)
			}
		}
	}
}

func TestTruncateBytesZeroMeansUnlimited(t *testing.T) {
	long := strings.Repeat("x", 10000)
	if got := TruncateBytes(long, 0); got != long {
		t.Fatal("max=0 should mean no limit")
	}
	if got := TruncateBytes(long, -5); got != long {
		t.Fatal("max<0 should mean no limit")
	}
}

func TestTruncateBytesEllipsisBudget(t *testing.T) {
	// When max is smaller than the ellipsis itself, appending the ellipsis must not in turn exceed the limit.
	got := TruncateBytes("abcdefgh", 1)
	if len(got) > 1 {
		t.Fatalf("with max=1 the result %q of length %d exceeds the limit", got, len(got))
	}
	// The normal case should carry an ellipsis.
	if got := TruncateBytes("abcdefgh", 5); !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("expected an ellipsis, got %q", got)
	}
}

func TestTruncateRunesCountsCharactersNotBytes(t *testing.T) {
	// The difference in measure from TruncateBytes must be kept: Telegram limits by characters,
	// and a byte measure would cut multi-byte messages down to a third.
	s := "€✓★⚠☂☀♥♦♣♠"
	got := TruncateRunes(s, 5)
	if n := utf8.RuneCountInString(got); n != 5 {
		t.Fatalf("expected 5 characters, got %d (%q)", n, got)
	}
	// The same string should be clearly shorter under the byte measure.
	if utf8.RuneCountInString(TruncateBytes(s, 5)) >= 5 {
		t.Fatal("the byte measure should not produce the same character count as the character measure")
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	got := OneLine("first line\n\nsecond line\twith tab   many spaces", 0)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("all whitespace should be collapsed, got %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("consecutive spaces should not be kept, got %q", got)
	}
	// It must still be readable and valid after truncation.
	got = OneLine("€✓★⚠☂☀♥♦♣♠", 4)
	if n := utf8.RuneCountInString(got); n != 4 {
		t.Fatalf("expected 4 characters, got %d (%q)", n, got)
	}
}

func TestTruncateHTMLNeverCutsTagInHalf(t *testing.T) {
	// Truncating HTML directly would cut fragments like `<a href="htt`, and the platform would reject the whole message.
	s := `<b>Title</b>body body body<a href="https://example.com/very/long/path">View details</a>`
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		if n := utf8.RuneCountInString(got); max > 0 && n > max {
			t.Fatalf("max=%d: result of %d characters exceeds the limit", max, n)
		}
		// The tail must not have an unclosed `<` (a `<` in the last part without a `>`).
		if lt := strings.LastIndex(got, "<"); lt >= 0 && !strings.Contains(got[lt:], ">") {
			t.Fatalf("max=%d: tail tag was cut off -> %q", max, got)
		}
	}
}

func TestAssetLineOmitsExcess(t *testing.T) {
	if got := assetLine(nil, 3); got != "" {
		t.Fatalf("no assets should return an empty string, got %q", got)
	}
	if got := assetLine([]string{"a", "b"}, 3); got != "a, b" {
		t.Fatalf("everything should be listed when under the limit, got %q", got)
	}
	// Beyond the limit the total must be noted, or readers do not know how many assets were not listed.
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if !strings.Contains(got, "5 total") {
		t.Fatalf("the total of 5 should be noted, got %q", got)
	}
}

func TestSeverityAndStatusLabels(t *testing.T) {
	if AtLeast("", "low") {
		t.Fatal("an empty severity has ordinal 0 and should be stopped by any threshold")
	}
	if !AtLeast("critical", "") {
		t.Fatal("an empty threshold should let it through")
	}
	if got := StatusLabel("fixed"); got != "Fixed" {
		t.Fatalf("unknown status mapping, got %q", got)
	}
	// Unknown statuses are echoed as is, without inventing a label.
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Fatalf("unknown statuses should be echoed as is, got %q", got)
	}
}

// TestTruncateHTMLNeverCutsEntity covers an omission the audit pointed out: truncation must avoid not only
// half tags but also cut-off HTML entities.
//
// Once `&amp;` is cut to `&amp`, a parser that only accepts entities may reject the **whole** message,
// and overlong digest messages are common, so the cost is too high.
func TestTruncateHTMLNeverCutsEntity(t *testing.T) {
	s := "aaaa&amp;bbbb&lt;cccc&quot;dddd"
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		// The tail must not contain an entity fragment with "an & but no matching ;".
		if amp := strings.LastIndex(got, "&"); amp >= 0 && !strings.Contains(got[amp:], ";") {
			t.Fatalf("max=%d: entity fragment left in the tail %q", max, got[amp:])
		}
		if strings.Contains(got, "&amp\x00") {
			t.Fatalf("max=%d: malformed entity appeared", max)
		}
	}
}
