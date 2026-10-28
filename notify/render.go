package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes truncates s to at most max bytes, guaranteeing valid UTF-8 without splitting characters.
//
// Why cutting on character boundaries is required: WeCom group robot markdown has a hard 4096 **byte** limit (not
// characters), and a CJK character takes 3 bytes. Slicing by bytes directly would split a character in half and produce invalid
// UTF-8, which the platform either rejects outright or displays as garbled boxes. The approach here is to back up from the budget position
// to the nearest rune start byte (utf8.RuneStart detects continuation bytes 0b10xxxxxx).
//
// max<=0 means no limit. An ellipsis is appended after truncation, unless max is too small to fit it.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max is shorter than the ellipsis: drop the ellipsis and truncate plainly, so the result does not exceed max.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine compresses multi-line text into one line: collapse all whitespace, then truncate by character count.
// Used for IM message title lines; summaries often contain newlines that would break table/title layout.
// max<=0 means no length limit.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes truncates s to at most max characters (not bytes), appending an ellipsis when it exceeds.
// max<=0 means no limit.
//
// The difference from TruncateBytes is the platform's measure: WeCom limits by bytes, Telegram by characters.
// Using the wrong measure raises no error; it just cuts messages far shorter than expected (one CJK character = 3 bytes,
// so 4096 bytes leaves only about 1365 characters), so both functions must be kept and chosen per channel.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML truncates an HTML fragment by character count while guaranteeing no half tags.
//
// Truncating HTML by characters directly can produce broken tags like `<a href="htt`, which platform parsers either
// reject outright or use to swallow the following body as an attribute value. The approach here: truncate by characters first,
// then check whether the tail has an unclosed `<`, and if so back up before it.
//
// No tag balancing (adding </b> etc.): Telegram's HTML parser closes unclosed tags automatically,
// and implementing balancing ourselves would mean handling quotes in attributes, comments and self-closing tags, a cost out of proportion to the benefit.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// If the tail is a fragment starting with `<` (no `>` after the last `<`), back up before the `<`.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// If the tail is a cut-off HTML entity (such as `&amp;` cut to `&amp`), back up as well.
	// An entity fragment can make a parser that only accepts entities reject the **whole message**; digest messages exceeding
	// the length limit are common, and losing the whole notification over that is not worth it.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount computes how many items fit **completely** within the budget, for packing digest messages by whole items.
//
// Why whole items instead of rendering everything and truncating: truncation makes the trailing items vanish,
// while their delivery records still get marked delivered; invisible in the message and in delivery history,
// the findings are simply gone. With whole-item packing, items that do not fit stay in the database for the next batch,
// and the kept the caller gets is the number of items this message actually delivered.
//
// Parameters: maxSize<=0 means no limit; reserve is the amount reserved for the message header/footer;
// size does the measuring (platforms differ: WeCom/DingTalk count bytes, Telegram counts characters;
// using the wrong measure raises no error, it just squeezes CJK messages far below the limit);
// render renders item idx into its actual text; length varies with content and cannot be estimated.
//
// It returns at least 1 (as long as there are items). Even an extremely long single item must be sent, with the caller's
// final truncation as the backstop; otherwise one overlong finding would leave the whole batch stuck forever.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize are the two measures for packItemCount, named to avoid bare
// func(s string) int closures at call sites, which would make it hard to see at a glance which measure is used.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine renders the asset list as one line of display text; beyond limit it omits the rest and notes the total.
// A finding may be anchored to dozens of assets, and listing them all would bloat the message.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + " and others, " + itoa(len(assets)) + " total"
}

// itoa is a short alias for strconv.Itoa, used only for building display text, to avoid importing strconv everywhere.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
