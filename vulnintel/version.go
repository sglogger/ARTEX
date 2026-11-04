package vulnintel

import (
	"strconv"
	"strings"
)

// CompareVersions orders two version strings: <0 when a<b, 0 when equal, >0 when a>b.
// It splits on . - _ + and on digit/letter boundaries, compares numeric parts as
// numbers, and treats a trailing pre-release tag ("1.0.0-rc1", "2.4beta") as lower
// than the release it precedes. It is deliberately lenient: vendor version schemes
// vary too much for strict semver.
func CompareVersions(a, b string) int {
	ta, tb := versionTokens(a), versionTokens(b)
	for i := 0; i < len(ta) || i < len(tb); i++ {
		switch {
		case i >= len(ta):
			return -tailSign(tb[i:])
		case i >= len(tb):
			return tailSign(ta[i:])
		}
		if c := compareToken(ta[i], tb[i]); c != 0 {
			return c
		}
	}
	return 0
}

// tailSign decides how extra trailing tokens rank against their absence:
// "1.0.1" > "1.0" but "1.0-rc1" < "1.0"; trailing zeros ("1.0.0" vs "1.0") are equal.
func tailSign(rest []string) int {
	for _, t := range rest {
		if isNum(t) {
			if strings.Trim(t, "0") != "" {
				return 1
			}
			continue
		}
		if isRelease(t) {
			continue
		}
		return -1
	}
	return 0
}

func compareToken(a, b string) int {
	an, bn := isNum(a), isNum(b)
	switch {
	case an && bn:
		x, _ := strconv.ParseUint(trimZeros(a), 10, 64)
		y, _ := strconv.ParseUint(trimZeros(b), 10, 64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case an:
		return 1 // a release number outranks a pre-release tag at the same position
	case bn:
		return -1
	}
	return strings.Compare(strings.ToLower(a), strings.ToLower(b))
}

func versionTokens(v string) []string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimPrefix(v, "v")
	var out []string
	var cur strings.Builder
	kind := 0 // 1 digit, 2 letter
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			if kind == 2 {
				flush()
			}
			kind = 1
			cur.WriteRune(r)
		case r >= 'a' && r <= 'z':
			if kind == 1 {
				flush()
			}
			kind = 2
			cur.WriteRune(r)
		default:
			flush()
			kind = 0
		}
	}
	flush()
	return out
}

func isNum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isRelease(s string) bool { return s == "final" || s == "release" || s == "ga" }

func trimZeros(s string) string {
	t := strings.TrimLeft(s, "0")
	if t == "" {
		return "0"
	}
	return t
}

// Range is a version interval; empty bounds are open.
type Range struct {
	StartIncl, StartExcl, EndIncl, EndExcl string
}

// Contains reports whether version v lies inside the range.
func (r Range) Contains(v string) bool {
	if r.StartIncl != "" && CompareVersions(v, r.StartIncl) < 0 {
		return false
	}
	if r.StartExcl != "" && CompareVersions(v, r.StartExcl) <= 0 {
		return false
	}
	if r.EndIncl != "" && CompareVersions(v, r.EndIncl) > 0 {
		return false
	}
	if r.EndExcl != "" && CompareVersions(v, r.EndExcl) >= 0 {
		return false
	}
	return true
}

// String renders the range for humans, e.g. ">=4.7.0 <7.1.4".
func (r Range) String() string {
	var parts []string
	if r.StartIncl != "" {
		parts = append(parts, ">="+r.StartIncl)
	}
	if r.StartExcl != "" {
		parts = append(parts, ">"+r.StartExcl)
	}
	if r.EndIncl != "" {
		parts = append(parts, "<="+r.EndIncl)
	}
	if r.EndExcl != "" {
		parts = append(parts, "<"+r.EndExcl)
	}
	if len(parts) == 0 {
		return "all versions"
	}
	return strings.Join(parts, " ")
}
