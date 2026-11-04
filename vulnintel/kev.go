package vulnintel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	cisaKEVURL   = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"
	euKEVURL     = "https://raw.githubusercontent.com/enisaeu/CNW/main/advisories/eukev/eukev.json"
	vulnCheckURL = "https://api.vulncheck.com/v3"
	epssURL      = "https://api.first.org/data/v1/epss"
	kevFeedTTL   = 6 * time.Hour
	lookupTTL    = 24 * time.Hour
	// vulnCheckCap bounds per-call VulnCheck requests; the API is queried per CVE.
	vulnCheckCap = 40
)

// KEVEntry is one catalog row, normalised across catalogs.
type KEVEntry struct {
	Catalog          string   `json:"catalog"`
	CVE              string   `json:"cve"`
	Vendor           string   `json:"vendor"`
	Product          string   `json:"product"`
	Name             string   `json:"name,omitempty"`
	Description      string   `json:"description,omitempty"`
	DateAdded        string   `json:"date_added,omitempty"`
	DueDate          string   `json:"due_date,omitempty"`
	Ransomware       string   `json:"ransomware,omitempty"`
	ExploitationType string   `json:"exploitation_type,omitempty"`
	CWEs             []string `json:"cwes,omitempty"`
	Notes            string   `json:"notes,omitempty"`
	Exploits         []string `json:"exploit_references,omitempty"`
}

func (e KEVEntry) mark() KEVMark {
	return KEVMark{Catalog: e.Catalog, DateAdded: e.DateAdded, DueDate: e.DueDate, Ransomware: e.Ransomware,
		ExploitationType: e.ExploitationType, Name: e.Name, ExploitReferences: capList(e.Exploits, 5)}
}

type kevSet struct {
	entries []KEVEntry
	byCVE   map[string][]int
	stale   bool
}

func indexKEV(entries []KEVEntry, stale bool) *kevSet {
	ks := &kevSet{entries: entries, byCVE: map[string][]int{}, stale: stale}
	for i, e := range entries {
		ks.byCVE[strings.ToUpper(e.CVE)] = append(ks.byCVE[strings.ToUpper(e.CVE)], i)
	}
	return ks
}

func (ks *kevSet) lookup(cve string) []KEVEntry {
	var out []KEVEntry
	for _, i := range ks.byCVE[strings.ToUpper(cve)] {
		out = append(out, ks.entries[i])
	}
	return out
}

// kevCatalog loads one downloadable catalog (CISA or EU).
func (s *Service) kevCatalog(ctx context.Context, cfg Settings, source string) (*kevSet, error) {
	switch source {
	case SourceCISAKEV:
		raw, stale, err := s.feed(ctx, cfg, "cisa_kev", s.base(SourceCISAKEV, cisaKEVURL), nil, kevFeedTTL)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Vulnerabilities []struct {
				CVEID       string   `json:"cveID"`
				Vendor      string   `json:"vendorProject"`
				Product     string   `json:"product"`
				Name        string   `json:"vulnerabilityName"`
				DateAdded   string   `json:"dateAdded"`
				Description string   `json:"shortDescription"`
				DueDate     string   `json:"dueDate"`
				Ransomware  string   `json:"knownRansomwareCampaignUse"`
				Notes       string   `json:"notes"`
				CWEs        []string `json:"cwes"`
			} `json:"vulnerabilities"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("parse CISA KEV: %w", err)
		}
		entries := make([]KEVEntry, 0, len(doc.Vulnerabilities))
		for _, v := range doc.Vulnerabilities {
			entries = append(entries, KEVEntry{Catalog: SourceCISAKEV, CVE: v.CVEID, Vendor: v.Vendor, Product: v.Product,
				Name: v.Name, Description: v.Description, DateAdded: v.DateAdded, DueDate: v.DueDate,
				Ransomware: v.Ransomware, CWEs: v.CWEs, Notes: v.Notes})
		}
		return indexKEV(entries, stale), nil
	case SourceEUKEV:
		raw, stale, err := s.feed(ctx, cfg, "eu_kev", s.base(SourceEUKEV, euKEVURL), nil, kevFeedTTL)
		if err != nil {
			return nil, err
		}
		var rows []struct {
			CVEID            string `json:"cveID"`
			EUVDID           string `json:"euvdID"`
			Vendor           string `json:"vendorProject"`
			Product          string `json:"product"`
			DateReported     string `json:"dateReported"`
			PatchedSince     string `json:"patchedSince"`
			Origin           string `json:"originSource"`
			Description      string `json:"shortDescription"`
			ExploitationType string `json:"exploitationType"`
			CWEs             string `json:"cwes"`
			Notes            string `json:"notes"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("parse EU KEV: %w", err)
		}
		entries := make([]KEVEntry, 0, len(rows))
		for _, v := range rows {
			name := v.EUVDID
			if v.Origin != "" {
				name = strings.TrimSpace(name + " (reported by " + v.Origin + ")")
			}
			entries = append(entries, KEVEntry{Catalog: SourceEUKEV, CVE: v.CVEID, Vendor: v.Vendor, Product: v.Product,
				Name: name, Description: v.Description, DateAdded: strings.ReplaceAll(v.DateReported, "/", "-"),
				ExploitationType: v.ExploitationType, CWEs: splitCSV(v.CWEs), Notes: v.Notes})
		}
		return indexKEV(entries, stale), nil
	}
	return nil, fmt.Errorf("unknown KEV catalog %q", source)
}

// vulnCheckKEV looks one CVE up in VulnCheck KEV (nil when not listed).
func (s *Service) vulnCheckKEV(ctx context.Context, cfg Settings, cve string) (*KEVMark, error) {
	cve = strings.ToUpper(cve)
	s.mu.Lock()
	ce, ok := s.vcCache[cve]
	s.mu.Unlock()
	if ok && ce.fresh(lookupTTL) {
		return ce.val, nil
	}
	var resp struct {
		Data []struct {
			Name       string `json:"vulnerabilityName"`
			Ransomware string `json:"knownRansomwareCampaignUse"`
			DateAdded  string `json:"date_added"`
			DueDate    string `json:"dueDate"`
			XDB        []struct {
				URL         string `json:"xdb_url"`
				ExploitType string `json:"exploit_type"`
			} `json:"vulncheck_xdb"`
			ReportedExploitation []struct {
				URL string `json:"url"`
			} `json:"vulncheck_reported_exploitation"`
		} `json:"data"`
	}
	header := map[string]string{"Authorization": "Bearer " + cfg.key(SourceVulnCheckKEV)}
	if err := s.getJSON(ctx, cfg, http.MethodGet, s.base(SourceVulnCheckKEV, vulnCheckURL)+"/index/vulncheck-kev?cve="+url.QueryEscape(cve), header, nil, &resp); err != nil {
		return nil, err
	}
	var m *KEVMark
	if len(resp.Data) > 0 {
		d := resp.Data[0]
		m = &KEVMark{Catalog: SourceVulnCheckKEV, Name: d.Name, Ransomware: d.Ransomware, DateAdded: dateOnly(d.DateAdded), DueDate: dateOnly(d.DueDate)}
		for _, x := range d.XDB {
			m.ExploitReferences = append(m.ExploitReferences, x.URL)
		}
		if len(m.ExploitReferences) == 0 {
			for _, r := range d.ReportedExploitation {
				m.ExploitReferences = append(m.ExploitReferences, r.URL)
			}
		}
		m.ExploitReferences = capList(uniq(m.ExploitReferences), 5)
	}
	s.mu.Lock()
	s.vcCache[cve] = cacheEntry[*KEVMark]{m, time.Now()}
	s.mu.Unlock()
	return m, nil
}

// epss fetches scores for CVE ids in batches.
func (s *Service) epss(ctx context.Context, cfg Settings, ids []string) (map[string]*EPSS, error) {
	out := map[string]*EPSS{}
	var missing []string
	s.mu.Lock()
	for _, id := range ids {
		id = strings.ToUpper(id)
		if ce, ok := s.epssCache[id]; ok && ce.fresh(lookupTTL) {
			if ce.val != nil {
				out[id] = ce.val
			}
			continue
		}
		missing = append(missing, id)
	}
	s.mu.Unlock()
	missing = uniq(missing)
	for start := 0; start < len(missing); start += 50 {
		end := min(start+50, len(missing))
		batch := missing[start:end]
		var resp struct {
			Data []struct {
				CVE        string `json:"cve"`
				EPSS       string `json:"epss"`
				Percentile string `json:"percentile"`
				Date       string `json:"date"`
			} `json:"data"`
		}
		if err := s.getJSON(ctx, cfg, http.MethodGet, s.base(SourceEPSS, epssURL)+"?cve="+strings.Join(batch, ","), nil, nil, &resp); err != nil {
			return out, err
		}
		got := map[string]*EPSS{}
		for _, d := range resp.Data {
			sc, _ := strconv.ParseFloat(d.EPSS, 64)
			pc, _ := strconv.ParseFloat(d.Percentile, 64)
			got[strings.ToUpper(d.CVE)] = &EPSS{Score: sc, Percentile: pc, Date: d.Date}
		}
		s.mu.Lock()
		for _, id := range batch {
			s.epssCache[id] = cacheEntry[*EPSS]{got[id], time.Now()}
			if got[id] != nil {
				out[id] = got[id]
			}
		}
		s.mu.Unlock()
	}
	return out, nil
}

// KEVQuery filters kev_search.
type KEVQuery struct {
	Query      string   `json:"query"`
	Vendor     string   `json:"vendor"`
	Product    string   `json:"product"`
	Since      string   `json:"since"` // YYYY-MM-DD, compares date_added
	Ransomware bool     `json:"ransomware_only"`
	Catalogs   []string `json:"catalogs"`
	Limit      int      `json:"limit"`
}

// KEVSearchResult is the kev_search answer.
type KEVSearchResult struct {
	Total   int            `json:"total"`
	Entries []KEVEntry     `json:"entries"`
	Sources []SourceStatus `json:"sources"`
}

// KEVSearch searches the downloadable catalogs; VulnCheck answers only exact CVE ids.
func (s *Service) KEVSearch(ctx context.Context, q KEVQuery) KEVSearchResult {
	cfg := s.settings()
	want := map[string]bool{}
	for _, c := range q.Catalogs {
		want[strings.TrimSpace(c)] = true
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	var res KEVSearchResult
	var hits []KEVEntry
	terms := strings.Fields(strings.ToLower(q.Query))
	for _, src := range []string{SourceCISAKEV, SourceEUKEV, SourceVulnCheckKEV} {
		if len(want) > 0 && !want[src] {
			continue
		}
		if ok, why := cfg.usable(src); !ok {
			res.Sources = append(res.Sources, SourceStatus{Source: src, Status: "skipped", Detail: why})
			continue
		}
		if src == SourceVulnCheckKEV {
			n := 0
			for _, t := range terms {
				if !strings.HasPrefix(t, "cve-") {
					continue
				}
				m, err := s.vulnCheckKEV(ctx, cfg, t)
				if err != nil {
					res.Sources = append(res.Sources, SourceStatus{Source: src, Status: "error", Detail: err.Error()})
					n = -1
					break
				}
				if m != nil {
					hits = append(hits, KEVEntry{Catalog: src, CVE: strings.ToUpper(t), Name: m.Name, DateAdded: m.DateAdded,
						DueDate: m.DueDate, Ransomware: m.Ransomware, Exploits: m.ExploitReferences})
					n++
				}
			}
			switch {
			case n >= 0 && len(terms) > 0 && strings.HasPrefix(terms[0], "cve-"):
				res.Sources = append(res.Sources, SourceStatus{Source: src, Status: "ok", Count: n})
			case n >= 0:
				res.Sources = append(res.Sources, SourceStatus{Source: src, Status: "not_applicable", Detail: "VulnCheck is only searched by exact CVE id"})
			}
			continue
		}
		ks, err := s.kevCatalog(ctx, cfg, src)
		if err != nil {
			res.Sources = append(res.Sources, SourceStatus{Source: src, Status: "error", Detail: err.Error()})
			continue
		}
		n := 0
		for _, e := range ks.entries {
			if !kevMatches(e, terms, q) {
				continue
			}
			hits = append(hits, e)
			n++
		}
		st := SourceStatus{Source: src, Status: "ok", Count: n}
		if ks.stale {
			st.Status, st.Detail = "stale", "refresh failed; using the cached catalog"
		}
		res.Sources = append(res.Sources, st)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].DateAdded > hits[j].DateAdded })
	res.Total = len(hits)
	res.Entries = capKEV(hits, q.Limit)
	return res
}

func kevMatches(e KEVEntry, terms []string, q KEVQuery) bool {
	if q.Vendor != "" && !strings.Contains(strings.ToLower(e.Vendor), strings.ToLower(q.Vendor)) {
		return false
	}
	if q.Product != "" && !strings.Contains(strings.ToLower(e.Product), strings.ToLower(q.Product)) {
		return false
	}
	if q.Since != "" && e.DateAdded < q.Since {
		return false
	}
	if q.Ransomware && !strings.EqualFold(e.Ransomware, "Known") {
		return false
	}
	hay := strings.ToLower(strings.Join([]string{e.CVE, e.Vendor, e.Product, e.Name, e.Description}, " "))
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

func capKEV(in []KEVEntry, n int) []KEVEntry {
	if len(in) > n {
		return in[:n]
	}
	return in
}

// kevProductMatches lists catalog rows naming the component, for products whose
// versions no range data could confirm (the agent must verify the version itself).
func kevProductMatches(ks *kevSet, names ...string) []KEVEntry {
	var out []KEVEntry
	for _, e := range ks.entries {
		hay := strings.ToLower(e.Vendor + " " + e.Product)
		for _, n := range names {
			n = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(n, "_", " ")))
			if len(n) >= 3 && strings.Contains(hay, n) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
