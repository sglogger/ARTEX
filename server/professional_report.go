package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

// The professional report combines the findings of one or more tasks into a
// client-ready document. Duplicates are consolidated through the finding groups
// exactly like the findings export, and the HTML is printed to PDF by the headless
// Chromium that ships with the Docker image (Playwright). Without a browser the
// HTML itself is offered, which any browser can print.

const professionalPDFTimeout = 2 * time.Minute

var (
	chromeOnce sync.Once
	chromePath string
)

// findChrome locates a Chromium able to print PDFs: ARTEX_CHROME, Playwright's
// headless shell or Chromium, then the usual system installs.
func findChrome() string {
	chromeOnce.Do(func() {
		if p := strings.TrimSpace(os.Getenv("ARTEX_CHROME")); p != "" {
			chromePath = p
			return
		}
		home, _ := os.UserHomeDir()
		roots := []string{os.Getenv("PLAYWRIGHT_BROWSERS_PATH"), filepath.Join(home, ".cache", "ms-playwright"), filepath.Join(home, "Library", "Caches", "ms-playwright")}
		patterns := []string{
			"chromium_headless_shell-*/chrome-headless-shell-*/chrome-headless-shell",
			"chromium_headless_shell-*/chrome-linux/headless_shell",
			"chromium-*/chrome-linux*/chrome",
			"chromium-*/chrome-mac*/Chromium.app/Contents/MacOS/Chromium",
		}
		for _, root := range roots {
			if root == "" {
				continue
			}
			for _, pat := range patterns {
				if m, _ := filepath.Glob(filepath.Join(root, pat)); len(m) > 0 {
					sort.Strings(m)
					chromePath = m[len(m)-1] // newest revision
					return
				}
			}
		}
		for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"} {
			if p, err := exec.LookPath(name); err == nil {
				chromePath = p
				return
			}
		}
		if runtime.GOOS == "darwin" {
			for _, p := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"} {
				if _, err := os.Stat(p); err == nil {
					chromePath = p
					return
				}
			}
		}
	})
	return chromePath
}

// renderPDF prints an HTML document with headless Chromium. The page is loaded
// from a local file and its CSP blocks every network request.
func renderPDF(ctx context.Context, html string) ([]byte, error) {
	chrome := findChrome()
	if chrome == "" {
		return nil, errNoChrome
	}
	dir, err := os.MkdirTemp("", "artex-report-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "report.html"), filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(in, []byte(html), 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, professionalPDFTimeout)
	defer cancel()
	args := []string{
		"--headless", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--disable-extensions",
		"--disable-background-networking", "--no-first-run", "--user-data-dir=" + filepath.Join(dir, "profile"),
		"--no-pdf-header-footer", "--print-to-pdf=" + out, "file://" + in,
	}
	cmd := exec.CommandContext(ctx, chrome, args...)
	if combined, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("chromium failed: %v: %s", err, lastLines(string(combined), 3))
	}
	return os.ReadFile(out)
}

var errNoChrome = fmt.Errorf("no Chromium found for PDF rendering; set ARTEX_CHROME or install Playwright's chromium, or download the HTML report and print it from a browser")

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

type professionalReportReq struct {
	TaskIDs         []string `json:"task_ids"`
	FindingIDs      []string `json:"finding_ids"`
	ExcludeStatuses []string `json:"exclude_statuses"`
	MinSeverity     string   `json:"min_severity"`
	Consolidated    *bool    `json:"consolidated"`
	IncludeEvidence *bool    `json:"include_evidence"`
	Title           string   `json:"title"`
	Client          string   `json:"client"`
	Author          string   `json:"author"`
	Classification  string   `json:"classification"`
	Language        string   `json:"language"`
	Summary         string   `json:"summary"`
	Format          string   `json:"format"` // pdf | html
}

var proSeverityRank = map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1, "info": 0, "": 0}

func parseIDList(raw []string) ([]int64, error) {
	var out []int64
	seen := map[int64]bool{}
	for _, s := range raw {
		id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("bad id %q", s)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// professionalReportInput collects findings and tasks for a request.
func (s *Server) professionalReportInput(req professionalReportReq) (report.ProInput, error) {
	taskIDs, err := parseIDList(req.TaskIDs)
	if err != nil {
		return report.ProInput{}, err
	}
	findingIDs, err := parseIDList(req.FindingIDs)
	if err != nil {
		return report.ProInput{}, err
	}
	if len(taskIDs) == 0 && len(findingIDs) == 0 {
		return report.ProInput{}, fmt.Errorf("select at least one task or finding")
	}
	var fs []*db.DBFinding
	seen := map[int64]bool{}
	add := func(list []*db.DBFinding) {
		for _, f := range list {
			if !seen[f.ID] {
				seen[f.ID] = true
				fs = append(fs, f)
			}
		}
	}
	if len(findingIDs) > 0 {
		list, err := s.m.pg.ListFindingsForExport(db.FindingFilter{}, findingIDs)
		if err != nil {
			return report.ProInput{}, err
		}
		add(list)
	}
	for _, id := range taskIDs {
		list, err := s.m.pg.ListFindingsForExport(db.FindingFilter{TaskID: strconv.FormatInt(id, 10)}, nil)
		if err != nil {
			return report.ProInput{}, err
		}
		add(list)
	}
	excluded := map[string]bool{}
	for _, st := range req.ExcludeStatuses {
		excluded[strings.TrimSpace(st)] = true
	}
	minRank := proSeverityRank[strings.ToLower(strings.TrimSpace(req.MinSeverity))]
	kept := fs[:0]
	for _, f := range fs {
		if excluded[f.Status] || proSeverityRank[strings.ToLower(f.Severity)] < minRank {
			continue
		}
		kept = append(kept, f)
	}
	fs = kept
	if req.Consolidated == nil || *req.Consolidated {
		var plan *findingCaseExportPlan
		if fs, plan, err = s.prepareFindingCaseExport(fs); err != nil {
			return report.ProInput{}, err
		}
		if fs, err = s.consolidateFindingCaseExport(fs, plan, false); err != nil {
			return report.ProInput{}, err
		}
	}

	// Tasks: the requested ones plus every task a selected finding came from.
	taskSet := map[int64]bool{}
	for _, id := range taskIDs {
		taskSet[id] = true
	}
	for _, f := range fs {
		if f.TaskID != nil {
			taskSet[*f.TaskID] = true
		}
	}
	ids := make([]int64, 0, len(taskSet))
	for id := range taskSet {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var tasks []report.ProTask
	for _, id := range ids {
		t, err := s.m.pg.GetTask(id)
		if err != nil || t == nil {
			continue
		}
		pt := report.ProTask{ID: t.ID, Name: t.Name, Description: t.Description, Goal: t.Goal, Status: t.Status, Ended: t.CompletedAt}
		pt.Started = t.FirstRunAt
		if pt.Started == nil {
			created := t.CreatedAt
			pt.Started = &created
		}
		if live, ok := s.m.Task(strconv.FormatInt(id, 10)); ok && live != nil {
			pt.Status = s.deriveTaskStatus(live)
		}
		if scope, err := s.m.Assets().ListTaskScope(id); err == nil {
			for _, sc := range scope {
				label := firstNonEmptyStr(sc.CompanyName, sc.Domain, sc.Net, sc.Value)
				if label != "" {
					pt.Scope = append(pt.Scope, label)
				}
			}
		}
		tasks = append(tasks, pt)
	}

	labels := map[int64]string{}
	for id, a := range s.resolveFindingAssets(fs) {
		labels[id] = assetLabel(a)
	}
	return report.ProInput{
		Title: req.Title, Client: req.Client, Author: req.Author, Classification: req.Classification,
		Language: req.Language, Summary: req.Summary, GeneratedAt: time.Now(),
		Tasks: tasks, Findings: fs, AssetLabels: labels,
		IncludeEvidence: req.IncludeEvidence == nil || *req.IncludeEvidence,
	}, nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var reportFileChars = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

func reportFileName(title, client, ext string) string {
	base := strings.Trim(reportFileChars.ReplaceAllString(firstNonEmptyStr(client, title, "report"), "-"), "-")
	if r := []rune(base); len(r) > 60 {
		base = string(r[:60])
	}
	return fmt.Sprintf("%s-pentest-report-%s.%s", base, time.Now().Format("20060102"), ext)
}

// createProfessionalReport renders the report as PDF (default) or HTML.
func (s *Server) createProfessionalReport(w http.ResponseWriter, r *http.Request) {
	var req professionalReportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	in, err := s.professionalReportInput(req)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	html, err := report.ProfessionalHTML(in)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if req.Format == "html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, reportFileName(req.Title, req.Client, "html")))
		_, _ = w.Write([]byte(html))
		return
	}
	pdf, err := renderPDF(r.Context(), html)
	if err == errNoChrome {
		writeErr(w, 501, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, reportFileName(req.Title, req.Client, "pdf")))
	_, _ = w.Write(pdf)
}

// professionalReportCapabilities tells the UI whether PDF rendering is available.
func (s *Server) professionalReportCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"pdf": findChrome() != ""})
}
