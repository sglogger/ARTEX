package report

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// The professional report is a print-ready HTML document (A4, cover page,
// management summary, scope per task, findings overview and details) that the
// server turns into a PDF with headless Chromium. Finding texts come from agents
// and targets: they are escaped by html/template, and Markdown is rendered
// without raw HTML, so nothing from a target can inject markup or scripts.

//go:embed professional.tmpl
var professionalTmpl string

// ProTask is one task covered by the report.
type ProTask struct {
	ID          int64
	Name        string
	Description string
	Goal        string
	Status      string
	Scope       []string
	Started     *time.Time
	Ended       *time.Time
}

// ProInput is everything the professional report needs.
type ProInput struct {
	Title           string
	Client          string
	Author          string
	Classification  string
	Language        string // en | de
	Summary         string // optional management summary written by a person (Markdown)
	GeneratedAt     time.Time
	Tasks           []ProTask
	Findings        []*db.DBFinding
	AssetLabels     map[int64]string
	IncludeEvidence bool
}

type proFinding struct {
	Ref       string
	Title     string
	Severity  string
	SevLabel  string
	Status    string
	Class     string
	Task      string
	TaskRef   string
	Assets    []string
	Found     string
	Summary   string
	Evidence  string
	Report    template.HTML
	Merged    int
	StaleNote bool
}

type proSeverity struct {
	Key, Label string
	Count      int
	Width      int // bar width in percent of the largest count
}

type proTaskView struct {
	ProTask
	Period   string
	Findings int
}

type proView struct {
	L               map[string]string
	Lang            string
	Title           string
	Client          string
	Author          string
	Classification  string
	Date            string
	Footer          string
	Summary         template.HTML
	Risk            string
	RiskLabel       string
	Total           int
	Severities      []proSeverity
	KeyFindings     []proFinding
	Tasks           []proTaskView
	Findings        []proFinding
	IncludeEvidence bool
	Period          string
}

var proSeverityOrder = []string{"critical", "high", "medium", "low", "info"}

func proSevKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, k := range proSeverityOrder {
		if s == k {
			return k
		}
	}
	return "info"
}

func proSevRank(s string) int {
	for i, k := range proSeverityOrder {
		if k == proSevKey(s) {
			return i
		}
	}
	return len(proSeverityOrder)
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

var headingTag = regexp.MustCompile(`<(/?)h([1-6])([ >])`)

// markdownHTML renders Markdown without raw HTML and demotes headings so a
// finding's own "## Impact" nests below the finding title.
func markdownHTML(src string, demote int) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	out := headingTag.ReplaceAllStringFunc(buf.String(), func(m string) string {
		p := headingTag.FindStringSubmatch(m)
		lvl, _ := strconv.Atoi(p[2])
		lvl = min(lvl+demote, 6)
		return fmt.Sprintf("<%sh%d%s", p[1], lvl, p[3])
	})
	return template.HTML(out) //nolint:gosec // goldmark escapes raw HTML (WithUnsafe is not set)
}

func fmtDate(t time.Time, lang string) string {
	if lang == "de" {
		return t.Format("02.01.2006")
	}
	return t.Format("2 January 2006")
}

// ProfessionalHTML renders the report document.
func ProfessionalHTML(in ProInput) (string, error) {
	lang := in.Language
	if _, ok := proLabels[lang]; !ok {
		lang = "en"
	}
	L := proLabels[lang]
	v := proView{
		L: L, Lang: lang,
		Title:           nz(strings.TrimSpace(in.Title), L["default_title"]),
		Client:          strings.TrimSpace(in.Client),
		Author:          nz(strings.TrimSpace(in.Author), "ARTEX"),
		Classification:  nz(strings.TrimSpace(in.Classification), L["confidential"]),
		Date:            fmtDate(in.GeneratedAt, lang),
		IncludeEvidence: in.IncludeEvidence,
	}
	v.Footer = v.Date
	if v.Client != "" {
		v.Footer = v.Client + " · " + v.Date
	}
	if s := strings.TrimSpace(in.Summary); s != "" {
		v.Summary = markdownHTML(s, 2)
	}

	fs := append([]*db.DBFinding(nil), in.Findings...)
	sort.SliceStable(fs, func(i, j int) bool {
		ri, rj := proSevRank(fs[i].Severity), proSevRank(fs[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return fs[i].CreatedAt.Before(fs[j].CreatedAt)
	})
	taskName := map[int64]string{}
	for _, t := range in.Tasks {
		taskName[t.ID] = fmt.Sprintf("#%d %s", t.ID, nz(t.Name, firstLineOf(t.Description)))
	}
	counts := map[string]int{}
	perTask := map[int64]int{}
	for i, f := range fs {
		key := proSevKey(f.Severity)
		counts[key]++
		pf := proFinding{
			Ref:      fmt.Sprintf("F-%02d", i+1),
			Title:    findingTitle(f),
			Severity: key,
			SevLabel: L["sev_"+key],
			Status:   L["status_"+nz(f.Status, "pending")],
			Class:    f.VulnClass,
			Found:    fmtDate(f.CreatedAt, lang),
			Summary:  strings.TrimSpace(f.Summary),
			Merged:   len(f.ExportMemberIDs),
		}
		if pf.Status == "" {
			pf.Status = f.Status
		}
		if f.TaskID != nil {
			perTask[*f.TaskID]++
			pf.TaskRef = fmt.Sprintf("#%d", *f.TaskID)
			pf.Task = nz(taskName[*f.TaskID], pf.TaskRef)
		}
		for _, id := range f.AssetIDs {
			if l := in.AssetLabels[id]; l != "" {
				pf.Assets = append(pf.Assets, l)
			}
		}
		if rep := strings.TrimSpace(f.Report); rep != "" {
			pf.Report = markdownHTML(rep, 3)
			pf.StaleNote = f.EvidenceVersion != f.ReportEvidenceVersion && f.ReportEvidenceVersion != 0
		}
		if in.IncludeEvidence || pf.Report == "" {
			pf.Evidence = strings.TrimSpace(f.Evidence)
		}
		v.Findings = append(v.Findings, pf)
	}
	v.Total = len(fs)
	maxCount := 0
	for _, k := range proSeverityOrder {
		maxCount = max(maxCount, counts[k])
	}
	for _, k := range proSeverityOrder {
		sv := proSeverity{Key: k, Label: L["sev_"+k], Count: counts[k]}
		if maxCount > 0 {
			sv.Width = counts[k] * 100 / maxCount
		}
		v.Severities = append(v.Severities, sv)
	}
	v.Risk, v.RiskLabel = "none", L["risk_none"]
	for _, k := range proSeverityOrder[:4] {
		if counts[k] > 0 {
			v.Risk, v.RiskLabel = k, L["sev_"+k]
			break
		}
	}
	for _, f := range v.Findings {
		if (f.Severity == "critical" || f.Severity == "high") && len(v.KeyFindings) < 8 {
			v.KeyFindings = append(v.KeyFindings, f)
		}
	}

	var first, last *time.Time
	for _, t := range in.Tasks {
		tv := proTaskView{ProTask: t, Findings: perTask[t.ID], Period: L["not_started"]}
		if t.Started != nil {
			end := L["ongoing"]
			if t.Ended != nil {
				end = fmtDate(*t.Ended, lang)
			}
			tv.Period = fmtDate(*t.Started, lang) + " – " + end
			if first == nil || t.Started.Before(*first) {
				first = t.Started
			}
		}
		if t.Ended != nil && (last == nil || t.Ended.After(*last)) {
			last = t.Ended
		}
		tv.Status = nz(L["task_"+t.Status], t.Status)
		v.Tasks = append(v.Tasks, tv)
	}
	if first != nil {
		end := v.Date
		if last != nil {
			end = fmtDate(*last, lang)
		}
		v.Period = fmtDate(*first, lang) + " – " + end
	}

	tmpl, err := template.New("report").Parse(professionalTmpl)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, v); err != nil {
		return "", err
	}
	return out.String(), nil
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

var proLabels = map[string]map[string]string{
	"en": {
		"default_title": "Penetration Test Report", "confidential": "Confidential", "client": "Client", "prepared_by": "Prepared by",
		"date": "Date", "classification": "Classification", "period": "Test period", "version": "Version",
		"toc": "Contents", "doc_control": "Document control",
		"mgmt_summary": "Management summary", "overall_risk": "Overall risk", "findings_total": "Findings in total",
		"risk_none": "No findings", "by_severity": "Findings by severity", "key_findings": "Key findings",
		"no_key_findings":   "No critical or high findings were identified.",
		"auto_summary_1":    "This report summarises the results of the security assessment of the systems listed in the scope section.",
		"auto_summary_2":    "The assessment identified %d findings. The highest rating is %s.",
		"auto_summary_none": "No security findings were confirmed within the tested scope and time frame.",
		"scope":             "Scope and engagement", "tasks": "Tasks", "task": "Task", "description": "Description", "goal": "Goal", "targets": "Targets", "status": "Status", "findings": "Findings",
		"methodology":  "Methodology",
		"method_intro": "The assessment was carried out with ARTEX, an autonomous penetration testing system in which AI agents plan, execute and verify test steps within the authorised scope. Every finding was recorded with evidence from the target; findings marked as merged were confirmed as the same defect and are reported once.",
		"phase_recon":  "Reconnaissance: discovery of domains, hosts, services and web endpoints within scope.",
		"phase_fp":     "Fingerprinting: identification of products, versions and configurations.",
		"phase_vuln":   "Vulnerability identification: manual-style checks by the agents and matching of detected versions against public vulnerability databases (NVD, OSV, CISA and ENISA KEV).",
		"phase_verify": "Verification: confirmation of findings with evidence, within the rules of engagement.",
		"phase_report": "Reporting: per-finding reports with impact, reproduction and remediation.",
		"rating":       "Severity rating", "rating_desc": "Description",
		"rate_critical": "Direct compromise of systems or data is possible with little effort, often without authentication. Remediate immediately.",
		"rate_high":     "Significant impact on confidentiality, integrity or availability. Remediate with high priority.",
		"rate_medium":   "Exploitation needs preconditions or has limited impact. Remediate in the regular cycle.",
		"rate_low":      "Minor weaknesses or hardening recommendations.",
		"rate_info":     "Observations without direct security impact.",
		"overview":      "Findings overview", "ref": "Ref.", "title": "Title", "severity": "Severity",
		"details": "Detailed findings", "class": "Class", "assets": "Affected assets", "found": "Found", "summary": "Summary",
		"evidence": "Evidence", "merged": "Merged from %d original findings.", "stale": "The evidence changed after this report was written.",
		"no_findings":     "No findings in the selected scope.",
		"disclaimer":      "Disclaimer",
		"disclaimer_text": "This assessment is a point-in-time review of the systems in scope, carried out with the permission of their owner. It cannot guarantee that no further vulnerabilities exist. Findings depend on the access, time frame and rules of engagement of the test. The report is intended for the named recipient only.",
		"not_started":     "not started", "ongoing": "ongoing", "page": "Page", "of": "of",
		"sev_critical": "Critical", "sev_high": "High", "sev_medium": "Medium", "sev_low": "Low", "sev_info": "Info",
		"status_pending": "Open", "status_in_progress": "In progress", "status_confirmed": "Confirmed", "status_resolved": "Resolved",
		"status_fixed": "Fixed", "status_false_positive": "False positive", "status_ignored": "Ignored", "status_duplicate": "Duplicate",
		"status_risk_accepted": "Risk accepted",
		"task_created":         "Created", "task_running": "Running", "task_paused": "Paused", "task_done": "Completed", "task_failed": "Failed",
		"task_timeout": "Timed out", "task_stopped": "Stopped", "task_queued": "Queued",
	},
	"de": {
		"default_title": "Penetrationstest-Bericht", "confidential": "Vertraulich", "client": "Auftraggeber", "prepared_by": "Erstellt von",
		"date": "Datum", "classification": "Klassifizierung", "period": "Testzeitraum", "version": "Version",
		"toc": "Inhalt", "doc_control": "Dokumentinformationen",
		"mgmt_summary": "Management Summary", "overall_risk": "Gesamtrisiko", "findings_total": "Findings gesamt",
		"risk_none": "Keine Findings", "by_severity": "Findings nach Schweregrad", "key_findings": "Wichtigste Findings",
		"no_key_findings":   "Es wurden keine kritischen oder hohen Findings festgestellt.",
		"auto_summary_1":    "Dieser Bericht fasst die Ergebnisse der Sicherheitsprüfung der im Abschnitt Scope aufgeführten Systeme zusammen.",
		"auto_summary_2":    "Die Prüfung ergab %d Findings. Die höchste Einstufung ist %s.",
		"auto_summary_none": "Im geprüften Scope und Zeitraum wurden keine Sicherheits-Findings bestätigt.",
		"scope":             "Scope und Auftrag", "tasks": "Tasks", "task": "Task", "description": "Beschreibung", "goal": "Auftrag", "targets": "Zielsysteme", "status": "Status", "findings": "Findings",
		"methodology":  "Vorgehen",
		"method_intro": "Die Prüfung wurde mit ARTEX durchgeführt, einem autonomen Penetrationstest-System, in dem KI-Agenten Testschritte innerhalb des freigegebenen Scopes planen, ausführen und verifizieren. Jedes Finding wurde mit Belegen vom Zielsystem erfasst; als zusammengeführt markierte Findings wurden als derselbe Mangel bestätigt und einmal berichtet.",
		"phase_recon":  "Aufklärung: Ermittlung von Domains, Hosts, Diensten und Web-Endpunkten im Scope.",
		"phase_fp":     "Fingerprinting: Erkennung von Produkten, Versionen und Konfigurationen.",
		"phase_vuln":   "Schwachstellenidentifikation: Prüfungen durch die Agenten und Abgleich erkannter Versionen mit öffentlichen Schwachstellendatenbanken (NVD, OSV, CISA- und ENISA-KEV).",
		"phase_verify": "Verifikation: Bestätigung der Findings mit Belegen im Rahmen der Rules of Engagement.",
		"phase_report": "Berichterstattung: Bericht pro Finding mit Auswirkung, Nachvollzug und Behebung.",
		"rating":       "Einstufung des Schweregrads", "rating_desc": "Beschreibung",
		"rate_critical": "Direkte Kompromittierung von Systemen oder Daten mit geringem Aufwand möglich, oft ohne Authentisierung. Sofort beheben.",
		"rate_high":     "Erhebliche Auswirkung auf Vertraulichkeit, Integrität oder Verfügbarkeit. Mit hoher Priorität beheben.",
		"rate_medium":   "Ausnutzung erfordert Voraussetzungen oder hat begrenzte Auswirkung. Im regulären Zyklus beheben.",
		"rate_low":      "Geringe Schwächen oder Härtungsempfehlungen.",
		"rate_info":     "Beobachtungen ohne direkte Sicherheitsauswirkung.",
		"overview":      "Übersicht der Findings", "ref": "Ref.", "title": "Titel", "severity": "Schweregrad",
		"details": "Findings im Detail", "class": "Kategorie", "assets": "Betroffene Assets", "found": "Gefunden", "summary": "Zusammenfassung",
		"evidence": "Beleg", "merged": "Zusammengeführt aus %d ursprünglichen Findings.", "stale": "Die Belege wurden nach Erstellung dieses Berichts geändert.",
		"no_findings":     "Keine Findings im gewählten Umfang.",
		"disclaimer":      "Haftungsausschluss",
		"disclaimer_text": "Diese Prüfung ist eine Momentaufnahme der Systeme im Scope und wurde mit Zustimmung ihres Eigentümers durchgeführt. Sie kann nicht garantieren, dass keine weiteren Schwachstellen bestehen. Die Ergebnisse hängen von Zugang, Zeitrahmen und Rules of Engagement des Tests ab. Der Bericht ist ausschließlich für den genannten Empfänger bestimmt.",
		"not_started":     "nicht gestartet", "ongoing": "laufend", "page": "Seite", "of": "von",
		"sev_critical": "Kritisch", "sev_high": "Hoch", "sev_medium": "Mittel", "sev_low": "Niedrig", "sev_info": "Info",
		"status_pending": "Offen", "status_in_progress": "In Bearbeitung", "status_confirmed": "Bestätigt", "status_resolved": "Erledigt",
		"status_fixed": "Behoben", "status_false_positive": "Fehlalarm", "status_ignored": "Ignoriert", "status_duplicate": "Duplikat",
		"status_risk_accepted": "Risiko akzeptiert",
		"task_created":         "Erstellt", "task_running": "Läuft", "task_paused": "Pausiert", "task_done": "Abgeschlossen", "task_failed": "Fehlgeschlagen",
		"task_timeout": "Zeitüberschreitung", "task_stopped": "Gestoppt", "task_queued": "In Warteschlange",
	},
}
