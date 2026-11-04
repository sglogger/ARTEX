//go:build ignore

package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex"
)

func TestChangelogServesEmbeddedFile(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.changelog(rec, httptest.NewRequest("GET", "/api/changelog", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("code=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != artex.Changelog || !strings.HasPrefix(artex.Changelog, "# Changelog") {
		t.Fatal("the response is not the embedded CHANGELOG.md")
	}
}

func TestLatestChangelogVersionSkipsUnreleased(t *testing.T) {
	text := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n## [0.4.3] - 2026-10-27\n\n## [0.4.2] - 2026-10-13\n"
	if got := latestChangelogVersion(text); got != "0.4.3" {
		t.Fatalf("latest = %q, want 0.4.3", got)
	}
	if got := latestChangelogVersion("# Changelog\n\n## [Unreleased]\n"); got != "" {
		t.Fatalf("no release yet = %q, want empty", got)
	}
	if changelogVersion == "" {
		t.Fatal("the embedded changelog has no released version")
	}
}
