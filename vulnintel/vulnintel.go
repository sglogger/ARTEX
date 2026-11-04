// Package vulnintel answers "which known vulnerabilities affect this component
// version?" from public vulnerability intelligence: the NVD CVE database, OSV,
// the CISA / ENISA EU / VulnCheck KEV catalogs, FIRST EPSS, and the WordPress
// specific Wordfence and WPScan databases. Every source can be switched on/off
// on its own; sources that need a key are skipped (and reported as skipped)
// until one is configured.
//
// Results always carry a per-source status, because "no vulnerabilities found"
// is only meaningful when every applicable source actually answered.
package vulnintel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Source identifiers. They double as setting keys and as the names reported in results.
const (
	SourceNVD          = "nvd"
	SourceOSV          = "osv"
	SourceCISAKEV      = "cisa_kev"
	SourceEUKEV        = "eu_kev"
	SourceVulnCheckKEV = "vulncheck_kev"
	SourceEPSS         = "epss"
	SourceWordfence    = "wordfence"
	SourceWPScan       = "wpscan"
)

// SourceInfo describes one source for the settings UI and the tool output.
type SourceInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Key         string `json:"key"` // "none" | "optional" | "required"
}

// Sources lists every supported source in display order.
var Sources = []SourceInfo{
	{SourceNVD, "NVD (NIST)", "CVE database with CPE version ranges, CVSS and references. A key raises the rate limit from 5 to 50 requests per 30 s.", "https://nvd.nist.gov/developers/request-an-api-key", "optional"},
	{SourceOSV, "OSV.dev", "Open source package vulnerabilities (npm, PyPI, Packagist, Maven, Go, RubyGems, crates.io, NuGet, Debian, Alpine …), including GitHub advisories.", "https://osv.dev", "none"},
	{SourceCISAKEV, "CISA KEV", "Known Exploited Vulnerabilities catalog of the US CISA.", "https://www.cisa.gov/known-exploited-vulnerabilities-catalog", "none"},
	{SourceEUKEV, "ENISA EU KEV", "Exploited vulnerabilities reported by EU CSIRTs (ENISA CNW).", "https://github.com/enisaeu/CNW", "none"},
	{SourceVulnCheckKEV, "VulnCheck KEV", "Extended KEV catalog with exploitation evidence and public exploit links.", "https://vulncheck.com", "required"},
	{SourceEPSS, "FIRST EPSS", "Probability that a CVE is exploited in the next 30 days.", "https://www.first.org/epss", "none"},
	{SourceWordfence, "Wordfence Intelligence", "WordPress core, plugin and theme vulnerabilities (feed v3, free API key).", "https://www.wordfence.com/threat-intel/", "required"},
	{SourceWPScan, "WPScan", "WordPress core, plugin and theme vulnerabilities (free plan: 25 requests/day).", "https://wpscan.com/api", "required"},
}

// SourceByID returns the source description, or false for an unknown id.
func SourceByID(id string) (SourceInfo, bool) {
	for _, s := range Sources {
		if s.ID == id {
			return s, true
		}
	}
	return SourceInfo{}, false
}

// Settings is the runtime configuration, read on every call so UI changes apply immediately.
type Settings struct {
	Enabled map[string]bool   // missing entry = enabled
	Keys    map[string]string // per-source API key
	Proxy   string            // optional egress proxy (http/https/socks5); empty = direct
}

func (s Settings) enabled(id string) bool {
	on, ok := s.Enabled[id]
	return !ok || on
}

func (s Settings) key(id string) string { return strings.TrimSpace(s.Keys[id]) }

// usable reports whether a source is switched on and has the key it requires.
func (s Settings) usable(id string) (bool, string) {
	if !s.enabled(id) {
		return false, "disabled in settings"
	}
	if info, _ := SourceByID(id); info.Key == "required" && s.key(id) == "" {
		return false, "no API key configured"
	}
	return true, ""
}

// Service is safe for concurrent use.
type Service struct {
	settings func() Settings
	cacheDir string
	// BaseURL overrides per source (tests only).
	BaseURL map[string]string

	mu        sync.Mutex
	clients   map[string]*http.Client
	feeds     map[string]*feedCache
	blobs     map[string]cachedBlob // lookup answers, mirrored to disk (cache.go)
	nvdGate   rateGate
	wordfence *wordfenceIndex
	wfLoaded  time.Time
}

// New builds a service. cacheDir holds downloaded feeds (KEV catalogs, the
// Wordfence feed) and lookup answers, so restarts and outages don't lose them;
// empty = memory only.
func New(settings func() Settings, cacheDir string) *Service {
	s := &Service{
		settings: settings,
		cacheDir: cacheDir,
		clients:  map[string]*http.Client{},
		feeds:    map[string]*feedCache{},
		blobs:    map[string]cachedBlob{},
	}
	go s.prune()
	return s
}

func (s *Service) base(source, def string) string {
	if u := s.BaseURL[source]; u != "" {
		return strings.TrimRight(u, "/")
	}
	return def
}

func (s *Service) client(proxy string) *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[proxy]; ok {
		return c
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		if u, err := url.Parse(proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	c := &http.Client{Transport: tr, Timeout: 90 * time.Second}
	s.clients[proxy] = c
	return c
}

// httpError keeps the status so callers can tell "not found" from failures.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("HTTP %d", e.status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

func isNotFound(err error) bool {
	he, ok := err.(*httpError)
	return ok && he.status == http.StatusNotFound
}

// getJSON performs a request and decodes a JSON response into out.
func (s *Service) getJSON(ctx context.Context, cfg Settings, method, rawURL string, header map[string]string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "artex-vulnintel/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := s.client(cfg.Proxy).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// rateGate spaces requests to one host (NVD allows 5 per 30 s without a key).
type rateGate struct {
	mu   sync.Mutex
	next time.Time
}

func (g *rateGate) wait(ctx context.Context, spacing time.Duration) error {
	g.mu.Lock()
	now := time.Now()
	at := g.next
	if at.Before(now) {
		at = now
	}
	g.next = at.Add(spacing)
	g.mu.Unlock()
	if d := time.Until(at); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

// feedCache holds one downloaded catalog, mirrored to disk.
type feedCache struct {
	mu  sync.Mutex
	raw []byte
	at  time.Time
}

// feed returns a cached download, refreshing it after ttl. A failed refresh
// falls back to the stale copy (reported through stale=true) rather than failing.
func (s *Service) feed(ctx context.Context, cfg Settings, name, rawURL string, header map[string]string, ttl time.Duration) (raw []byte, stale bool, err error) {
	s.mu.Lock()
	fc := s.feeds[name]
	if fc == nil {
		fc = &feedCache{}
		s.feeds[name] = fc
	}
	s.mu.Unlock()
	fc.mu.Lock()
	defer fc.mu.Unlock()
	path := ""
	if s.cacheDir != "" {
		path = filepath.Join(s.cacheDir, name+".json")
	}
	if fc.raw == nil && path != "" {
		if st, statErr := os.Stat(path); statErr == nil {
			if b, readErr := os.ReadFile(path); readErr == nil {
				fc.raw, fc.at = b, st.ModTime()
			}
		}
	}
	live := isLive(ctx)
	if !live && fc.raw != nil && time.Since(fc.at) < ttl {
		return fc.raw, false, nil
	}
	b, dlErr := s.download(ctx, cfg, rawURL, header)
	if dlErr != nil {
		if fc.raw != nil && !live {
			return fc.raw, true, nil
		}
		return nil, false, dlErr
	}
	fc.raw, fc.at = b, time.Now()
	if path != "" {
		if mkErr := os.MkdirAll(s.cacheDir, 0o755); mkErr == nil {
			tmp := path + ".tmp"
			if os.WriteFile(tmp, b, 0o644) == nil {
				_ = os.Rename(tmp, path)
			}
		}
	}
	return b, false, nil
}

func (s *Service) download(ctx context.Context, cfg Settings, rawURL string, header map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-vulnintel/1")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	c := *s.client(cfg.Proxy)
	c.Timeout = 5 * time.Minute // feeds can be large (Wordfence)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	return io.ReadAll(resp.Body)
}

// ---- result types ----

// Component is one piece of fingerprinted software.
type Component struct {
	// Kind: wordpress_core | wordpress_plugin | wordpress_theme | product | package
	Kind      string `json:"kind"`
	Name      string `json:"name"`                // product name, package name or WordPress slug
	Version   string `json:"version"`             // detected version; empty = list everything known
	Vendor    string `json:"vendor,omitempty"`    // CPE vendor (product kind), optional
	Ecosystem string `json:"ecosystem,omitempty"` // OSV ecosystem (package kind), e.g. npm, PyPI, Packagist
}

// KEVMark records that a CVE is listed in a known-exploited catalog.
type KEVMark struct {
	Catalog           string   `json:"catalog"`
	DateAdded         string   `json:"date_added,omitempty"`
	DueDate           string   `json:"due_date,omitempty"`
	Ransomware        string   `json:"ransomware,omitempty"`
	ExploitationType  string   `json:"exploitation_type,omitempty"`
	Name              string   `json:"name,omitempty"`
	ExploitReferences []string `json:"exploit_references,omitempty"`
}

// EPSS is FIRST's exploit prediction for a CVE.
type EPSS struct {
	Score      float64 `json:"score"`
	Percentile float64 `json:"percentile"`
	Date       string  `json:"date,omitempty"`
}

// Vuln is one vulnerability merged across sources (keyed by CVE when one exists).
type Vuln struct {
	ID          string    `json:"id"`
	Aliases     []string  `json:"aliases,omitempty"`
	Title       string    `json:"title,omitempty"`
	Severity    string    `json:"severity,omitempty"`
	CVSS        float64   `json:"cvss,omitempty"`
	Vector      string    `json:"cvss_vector,omitempty"`
	CWE         []string  `json:"cwe,omitempty"`
	Affected    []string  `json:"affected,omitempty"`
	FixedIn     string    `json:"fixed_in,omitempty"`
	Published   string    `json:"published,omitempty"`
	Sources     []string  `json:"sources"`
	KEV         []KEVMark `json:"kev,omitempty"`
	EPSS        *EPSS     `json:"epss,omitempty"`
	ExploitRefs []string  `json:"exploit_refs,omitempty"`
	References  []string  `json:"references,omitempty"`
}

// SourceStatus says what one source contributed to a result.
type SourceStatus struct {
	Source string `json:"source"`
	Status string `json:"status"` // ok | stale | skipped | error | not_applicable
	Detail string `json:"detail,omitempty"`
	Count  int    `json:"count,omitempty"`
}

// ComponentResult is the answer for one component.
type ComponentResult struct {
	Component Component      `json:"component"`
	Vulns     []Vuln         `json:"vulnerabilities"`
	Sources   []SourceStatus `json:"sources"`
	// KEVProductMatches are KEV entries whose vendor/product names match the
	// component but whose version could not be checked against a range.
	KEVProductMatches []KEVEntry `json:"kev_product_matches,omitempty"`
	Complete          bool       `json:"complete"`
	Note              string     `json:"note,omitempty"`
}

// merger collects vulnerabilities from several sources under one id.
type merger struct {
	byID  map[string]*Vuln
	alias map[string]string
	order []string
}

func newMerger() *merger { return &merger{byID: map[string]*Vuln{}, alias: map[string]string{}} }

func (m *merger) add(v Vuln) {
	key := primaryID(v)
	if canon, ok := m.alias[key]; ok {
		key = canon
	}
	for _, a := range v.Aliases {
		if canon, ok := m.alias[strings.ToUpper(a)]; ok {
			key = canon
		}
	}
	cur, ok := m.byID[key]
	if !ok {
		v.ID = key
		cp := v
		m.byID[key] = &cp
		m.order = append(m.order, key)
		m.alias[key] = key
		m.alias[strings.ToUpper(v.ID)] = key
		for _, a := range v.Aliases {
			m.alias[strings.ToUpper(a)] = key
		}
		return
	}
	mergeInto(cur, v)
	for _, a := range v.Aliases {
		m.alias[strings.ToUpper(a)] = key
	}
}

func mergeInto(cur *Vuln, v Vuln) {
	cur.Aliases = uniq(append(append(cur.Aliases, v.Aliases...), v.ID), cur.ID)
	if cur.Title == "" {
		cur.Title = v.Title
	}
	if v.CVSS > cur.CVSS {
		cur.CVSS, cur.Vector, cur.Severity = v.CVSS, v.Vector, v.Severity
	}
	if cur.Severity == "" {
		cur.Severity = v.Severity
	}
	cur.CWE = uniq(append(cur.CWE, v.CWE...))
	cur.Affected = uniq(append(cur.Affected, v.Affected...))
	if cur.FixedIn == "" {
		cur.FixedIn = v.FixedIn
	}
	if cur.Published == "" {
		cur.Published = v.Published
	}
	cur.Sources = uniq(append(cur.Sources, v.Sources...))
	cur.ExploitRefs = uniq(append(cur.ExploitRefs, v.ExploitRefs...))
	cur.References = capList(uniq(append(cur.References, v.References...)), 8)
}

// primaryID prefers a CVE id so the same issue from several sources merges.
func primaryID(v Vuln) string {
	if strings.HasPrefix(strings.ToUpper(v.ID), "CVE-") {
		return strings.ToUpper(v.ID)
	}
	for _, a := range v.Aliases {
		if strings.HasPrefix(strings.ToUpper(a), "CVE-") {
			return strings.ToUpper(a)
		}
	}
	return v.ID
}

func (m *merger) list() []Vuln {
	out := make([]Vuln, 0, len(m.order))
	for _, k := range m.order {
		v := *m.byID[k]
		v.Aliases = uniq(v.Aliases, v.ID)
		out = append(out, v)
	}
	return out
}

func uniq(in []string, drop ...string) []string {
	seen := map[string]bool{}
	for _, d := range drop {
		seen[strings.ToUpper(d)] = true
	}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToUpper(s)] {
			continue
		}
		seen[strings.ToUpper(s)] = true
		out = append(out, s)
	}
	return out
}

func capList(in []string, n int) []string {
	if len(in) > n {
		return in[:n]
	}
	return in
}

// sortVulns orders by "most urgent first": KEV-listed, then CVSS, then EPSS.
func sortVulns(vs []Vuln) {
	sort.SliceStable(vs, func(i, j int) bool {
		ki, kj := len(vs[i].KEV) > 0, len(vs[j].KEV) > 0
		if ki != kj {
			return ki
		}
		if vs[i].CVSS != vs[j].CVSS {
			return vs[i].CVSS > vs[j].CVSS
		}
		return epssScore(vs[i]) > epssScore(vs[j])
	})
}

func epssScore(v Vuln) float64 {
	if v.EPSS == nil {
		return 0
	}
	return v.EPSS.Score
}

// severityFromScore maps a CVSS base score to its qualitative rating.
func severityFromScore(score float64) string {
	switch {
	case score >= 9:
		return "CRITICAL"
	case score >= 7:
		return "HIGH"
	case score >= 4:
		return "MEDIUM"
	case score > 0:
		return "LOW"
	}
	return ""
}

func cveIDs(vs []Vuln) []string {
	var out []string
	for _, v := range vs {
		if strings.HasPrefix(v.ID, "CVE-") {
			out = append(out, v.ID)
		}
	}
	return out
}
