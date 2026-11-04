//go:build ignore

package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func professionalReportServer(t *testing.T) (*Server, *db.Task, *db.Task) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("postgres unavailable")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	a, err := pg.CreateTaskWithOptions("WordPress check", "Target: https://stage.example", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := pg.CreateTaskWithOptions("API check", "Target: https://api.example", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pg.Exec(`DELETE FROM findings WHERE task_id IN ($1,$2)`, a.ID, b.ID)
		_ = pg.DeleteTask(a.ID)
		_ = pg.DeleteTask(b.ID)
	})
	if _, err := pg.AddFinding(a.ID, 0, "Vulnerable component", "Outdated WordPress core", "high", "WordPress 6.9.4", "generator meta tag", "w1", nil); err != nil {
		t.Fatal(err)
	}
	fp, err := pg.AddFinding(a.ID, 0, "XSS", "Reflected XSS that was a false alarm", "medium", "", "", "w1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.SetFindingStatus(fp, db.FindingFalsePositive); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.AddFinding(b.ID, 0, "Misconfiguration", "Missing HSTS header", "low", "", "curl -I", "w2", nil); err != nil {
		t.Fatal(err)
	}
	return &Server{m: &Manager{pg: pg, assets: pg.Assets(), dir: t.TempDir()}}, a, b
}

func postProfessionalReport(s *Server, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.createProfessionalReport(w, httptest.NewRequest("POST", "/api/reports/professional", strings.NewReader(body)))
	return w
}

func TestProfessionalReportCombinesTasks(t *testing.T) {
	s, a, b := professionalReportServer(t)
	w := postProfessionalReport(s, fmt.Sprintf(`{"task_ids":["%d","%d"],"exclude_statuses":["false_positive"],"format":"html","client":"Example AG","language":"de"}`, a.ID, b.ID))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type %q", ct)
	}
	if d := w.Header().Get("Content-Disposition"); !strings.Contains(d, "Example-AG-pentest-report-") || !strings.HasSuffix(d, `.html"`) {
		t.Errorf("file name %q", d)
	}
	html := w.Body.String()
	for _, want := range []string{"Outdated WordPress core", "Missing HSTS header", fmt.Sprintf("Task #%d", a.ID), fmt.Sprintf("Task #%d", b.ID), "Example AG", "Die Prüfung ergab 2 Findings"} {
		if !strings.Contains(html, want) {
			t.Errorf("report misses %q", want)
		}
	}
	if strings.Contains(html, "false alarm") {
		t.Error("excluded false positive is in the report")
	}
}

func TestProfessionalReportFilters(t *testing.T) {
	s, a, b := professionalReportServer(t)
	w := postProfessionalReport(s, fmt.Sprintf(`{"task_ids":["%d","%d"],"min_severity":"high","format":"html"}`, a.ID, b.ID))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if html := w.Body.String(); !strings.Contains(html, "Outdated WordPress core") || strings.Contains(html, "Missing HSTS header") {
		t.Error("min_severity=high must keep only the high finding")
	}
	if w := postProfessionalReport(s, `{"format":"html"}`); w.Code != 400 {
		t.Errorf("empty selection accepted: %d", w.Code)
	}
	if w := postProfessionalReport(s, `{"task_ids":["x"]}`); w.Code != 400 {
		t.Errorf("bad task id accepted: %d", w.Code)
	}
}

// Runs only where a Chromium is installed (the Docker image ships one).
func TestRenderPDFWithChromium(t *testing.T) {
	if findChrome() == "" {
		t.Skip("no Chromium available")
	}
	pdf, err := renderPDF(t.Context(), `<!doctype html><html><body><h1>Report äöü</h1></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(pdf), "%PDF-") || len(pdf) < 1000 {
		t.Fatalf("not a PDF: %d bytes", len(pdf))
	}
}
