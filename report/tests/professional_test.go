//go:build ignore

package report

import (
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func proFixture() ProInput {
	t1, t2 := int64(5), int64(7)
	started := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	return ProInput{
		Title: "Web assessment", Client: "Ascano AG", Author: "S. Tester", Language: "en", GeneratedAt: started.Add(48 * time.Hour),
		Tasks: []ProTask{
			{ID: 5, Description: "WordPress check", Goal: "Target: https://stage.example", Status: "done", Scope: []string{"stage.example"}, Started: &started},
			{ID: 7, Description: "API check", Status: "running"},
		},
		Findings: []*db.DBFinding{
			{ID: 1, TaskID: &t1, Name: "Outdated WordPress core", Severity: "high", Status: "confirmed", AssetIDs: []int64{9},
				Summary: "WordPress 6.9.4 <script>alert(1)</script>", Report: "## Impact\n\nRemote code execution.\n\n<img src=x onerror=alert(2)>",
				Notes:     "Client confirmed the **staging** host is internet-facing.\n\n<iframe src=x></iframe>",
				CreatedAt: started},
			{ID: 2, TaskID: &t2, Name: "Missing HSTS", Severity: "low", Status: "pending", Evidence: "curl -I https://api.example", CreatedAt: started},
			{ID: 3, TaskID: &t1, Name: "SQL injection", Severity: "critical", Status: "confirmed", ExportMemberIDs: []int64{3, 4}, CreatedAt: started},
		},
		AssetLabels:     map[int64]string{9: "https://stage.example/"},
		IncludeEvidence: true,
	}
}

func TestProfessionalHTMLStructure(t *testing.T) {
	html, err := ProfessionalHTML(proFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Web assessment", "Ascano AG", "Management summary", "Scope and engagement", "Task #5", "Task #7",
		"stage.example", "F-01", "SQL injection", "Merged from 2 original findings.", "https://stage.example/",
		"The assessment identified 3 findings. The highest rating is Critical.", "curl -I https://api.example", "No detailed report has been written for this finding yet",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("report misses %q", want)
		}
	}
	// Critical sorts first, so the SQL injection is F-01 and the low finding is last.
	if strings.Index(html, "F-01</span><h3>SQL injection") < 0 || strings.Index(html, "F-03</span><h3>Missing HSTS") < 0 {
		t.Error("findings are not numbered by severity")
	}
	// The finding's own "## Impact" is demoted below the finding title.
	if !strings.Contains(html, "<h5>Impact</h5>") {
		t.Error("report headings were not demoted")
	}
}

// Finding texts come from agents and targets and must never become markup.
func TestProfessionalHTMLEscapesFindingContent(t *testing.T) {
	html, err := ProfessionalHTML(proFixture())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>alert(1)") || strings.Contains(html, "<img src=x") {
		t.Fatal("raw HTML from a finding reached the report")
	}
	if !strings.Contains(html, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("summary should be shown escaped")
	}
}

func TestProfessionalHTMLGermanAndEmpty(t *testing.T) {
	in := proFixture()
	in.Language, in.Findings = "de", nil
	html, err := ProfessionalHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Management Summary", "Keine Findings", "Im geprüften Scope und Zeitraum wurden keine Sicherheits-Findings bestätigt.", "05.11.2026", "Seite"} {
		if !strings.Contains(html, want) {
			t.Errorf("German report misses %q", want)
		}
	}
}

func TestProfessionalHTMLIncludesNotes(t *testing.T) {
	html, err := ProfessionalHTML(proFixture())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "<h4>Tester notes</h4>") || !strings.Contains(html, "Client confirmed the <strong>staging</strong> host") {
		t.Error("tester notes missing or not rendered as Markdown")
	}
	if strings.Contains(html, "<iframe") {
		t.Error("raw HTML from notes reached the report")
	}
}
