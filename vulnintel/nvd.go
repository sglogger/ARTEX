package vulnintel

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	nvdBase     = "https://services.nvd.nist.gov/rest/json"
	nvdCacheTTL = 6 * time.Hour
	nvdPageSize = 2000
	nvdMaxPages = 5
)

// NVD API 2.0 wire format (only the fields we read).
type nvdResponse struct {
	TotalResults    int `json:"totalResults"`
	Vulnerabilities []struct {
		CVE nvdWireCVE `json:"cve"`
	} `json:"vulnerabilities"`
}

type nvdCVSS struct {
	CVSSData struct {
		BaseScore    float64 `json:"baseScore"`
		BaseSeverity string  `json:"baseSeverity"`
		VectorString string  `json:"vectorString"`
	} `json:"cvssData"`
	BaseSeverity string `json:"baseSeverity"` // CVSS v2 keeps it here
}

type nvdWireCVE struct {
	ID           string `json:"id"`
	Published    string `json:"published"`
	VulnStatus   string `json:"vulnStatus"`
	Descriptions []struct {
		Lang  string `json:"lang"`
		Value string `json:"value"`
	} `json:"descriptions"`
	Metrics struct {
		V40 []nvdCVSS `json:"cvssMetricV40"`
		V31 []nvdCVSS `json:"cvssMetricV31"`
		V30 []nvdCVSS `json:"cvssMetricV30"`
		V2  []nvdCVSS `json:"cvssMetricV2"`
	} `json:"metrics"`
	Weaknesses []struct {
		Description []struct {
			Value string `json:"value"`
		} `json:"description"`
	} `json:"weaknesses"`
	Configurations []struct {
		Nodes []struct {
			CPEMatch []struct {
				Vulnerable            bool   `json:"vulnerable"`
				Criteria              string `json:"criteria"`
				VersionStartIncluding string `json:"versionStartIncluding"`
				VersionStartExcluding string `json:"versionStartExcluding"`
				VersionEndIncluding   string `json:"versionEndIncluding"`
				VersionEndExcluding   string `json:"versionEndExcluding"`
			} `json:"cpeMatch"`
		} `json:"nodes"`
	} `json:"configurations"`
	References []struct {
		URL  string   `json:"url"`
		Tags []string `json:"tags"`
	} `json:"references"`
}

// nvdCVE is the parsed, cacheable form of one CVE record.
type nvdCVE struct {
	ID, Published, Status, Description string
	Score                              float64
	Severity, Vector                   string
	CWE                                []string
	Matches                            []cpeMatch
	Refs                               []string
	Exploits                           []string
}

type cpeMatch struct {
	Part, Vendor, Product, Version string
	Range                          Range
}

func (m cpeMatch) contains(version string) bool {
	switch m.Version {
	case "*", "":
		return m.Range.Contains(version)
	case "-":
		return false
	}
	return CompareVersions(m.Version, version) == 0
}

func (m cpeMatch) label() string {
	if m.Version != "*" && m.Version != "" && m.Version != "-" {
		return "=" + m.Version
	}
	return m.Range.String()
}

func parseNVD(w nvdWireCVE) nvdCVE {
	c := nvdCVE{ID: w.ID, Published: w.Published, Status: w.VulnStatus}
	for _, d := range w.Descriptions {
		if d.Lang == "en" {
			c.Description = d.Value
			break
		}
	}
	for _, set := range [][]nvdCVSS{w.Metrics.V40, w.Metrics.V31, w.Metrics.V30, w.Metrics.V2} {
		if len(set) == 0 {
			continue
		}
		m := set[0]
		c.Score, c.Vector = m.CVSSData.BaseScore, m.CVSSData.VectorString
		c.Severity = firstNonEmpty(m.CVSSData.BaseSeverity, m.BaseSeverity, severityFromScore(c.Score))
		break
	}
	for _, wk := range w.Weaknesses {
		for _, d := range wk.Description {
			if strings.HasPrefix(d.Value, "CWE-") {
				c.CWE = append(c.CWE, d.Value)
			}
		}
	}
	c.CWE = uniq(c.CWE)
	for _, cfg := range w.Configurations {
		for _, n := range cfg.Nodes {
			for _, m := range n.CPEMatch {
				if !m.Vulnerable {
					continue
				}
				f := strings.Split(m.Criteria, ":")
				if len(f) < 6 {
					continue
				}
				c.Matches = append(c.Matches, cpeMatch{
					Part: f[2], Vendor: f[3], Product: f[4], Version: f[5],
					Range: Range{m.VersionStartIncluding, m.VersionStartExcluding, m.VersionEndIncluding, m.VersionEndExcluding},
				})
			}
		}
	}
	for _, r := range w.References {
		exploit := false
		for _, t := range r.Tags {
			if t == "Exploit" {
				exploit = true
			}
		}
		if exploit {
			c.Exploits = append(c.Exploits, r.URL)
		} else {
			c.Refs = append(c.Refs, r.URL)
		}
	}
	return c
}

func (c nvdCVE) vuln() Vuln {
	return Vuln{
		ID: c.ID, Title: truncate(c.Description, 400), CVSS: c.Score, Severity: c.Severity, Vector: c.Vector,
		CWE: c.CWE, Published: dateOnly(c.Published), Sources: []string{SourceNVD},
		ExploitRefs: capList(c.Exploits, 6), References: capList(c.Refs, 5),
	}
}

func (s *Service) nvdHeader(cfg Settings) map[string]string {
	if k := cfg.key(SourceNVD); k != "" {
		return map[string]string{"apiKey": k}
	}
	return nil
}

// nvdGet throttles to the documented public limits (5 / 30 s keyless, 50 / 30 s with a key).
func (s *Service) nvdGet(ctx context.Context, cfg Settings, path string, q url.Values, out any) error {
	spacing := 6500 * time.Millisecond
	if cfg.key(SourceNVD) != "" {
		spacing = 700 * time.Millisecond
	}
	if err := s.nvdGate.wait(ctx, spacing); err != nil {
		return err
	}
	return s.getJSON(ctx, cfg, http.MethodGet, s.base(SourceNVD, nvdBase)+path+"?"+q.Encode(), s.nvdHeader(cfg), nil, out)
}

// cpeProduct is one distinct part:vendor:product from the CPE dictionary.
type cpeProduct struct {
	Part, Vendor, Product, TargetSW string
	Hits                            int
}

func normalizeCPE(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer(" ", "_", "-", "_", ".", "_").Replace(s)
}

// maxCPEProducts bounds how many vendors are queried for one product name: the
// NVD often lists the same software under a historical and a current vendor
// (nginx: igor_sysoev and f5), and only the union is complete.
const maxCPEProducts = 3

// resolveCPE finds the CPE vendor/product pairs for a free-text product name.
// preferTarget ("wordpress") favours CPEs whose target_sw names that platform.
func (s *Service) resolveCPE(ctx context.Context, cfg Settings, name, vendor, preferTarget string) ([]cpeProduct, error) {
	key := strings.ToLower(name + "|" + vendor + "|" + preferTarget)
	s.mu.Lock()
	ce, ok := s.cpeCache[key]
	s.mu.Unlock()
	var cands []cpeProduct
	if ok && ce.fresh(24*time.Hour) {
		cands = ce.val
	} else {
		// Exact product name across all vendors first; free-text search only when
		// the name is not a CPE product (e.g. "slider revolution" vs slider_revolution).
		vendorPart := "*"
		if vendor != "" {
			vendorPart = normalizeCPE(vendor)
		}
		queries := []url.Values{{"cpeMatchString": {"cpe:2.3:*:" + vendorPart + ":" + normalizeCPE(name)}, "resultsPerPage": {"10000"}}}
		if vendor == "" {
			queries = append(queries, url.Values{"keywordSearch": {strings.ReplaceAll(name, "_", " ")}, "resultsPerPage": {"2000"}})
		}
		type cpeResp struct {
			Products []struct {
				CPE struct {
					CPEName string `json:"cpeName"`
				} `json:"cpe"`
			} `json:"products"`
		}
		var resp cpeResp
		for _, q := range queries {
			if err := s.nvdGet(ctx, cfg, "/cpes/2.0", q, &resp); err != nil {
				return nil, err
			}
			if len(resp.Products) > 0 {
				break
			}
		}
		agg := map[string]*cpeProduct{}
		var order []string
		for _, p := range resp.Products {
			f := strings.Split(p.CPE.CPEName, ":")
			if len(f) < 11 {
				continue
			}
			k := f[2] + ":" + f[3] + ":" + f[4]
			if agg[k] == nil {
				agg[k] = &cpeProduct{Part: f[2], Vendor: f[3], Product: f[4], TargetSW: f[10]}
				order = append(order, k)
			}
			agg[k].Hits++
			if f[10] != "*" && f[10] != "-" {
				agg[k].TargetSW = f[10]
			}
		}
		for _, k := range order {
			cands = append(cands, *agg[k])
		}
		s.mu.Lock()
		s.cpeCache[key] = cacheEntry[[]cpeProduct]{cands, time.Now()}
		s.mu.Unlock()
	}
	want := normalizeCPE(name)
	score := func(c cpeProduct) int {
		n := c.Hits
		if c.Product == want {
			n += 100000
		} else if strings.Contains(c.Product, want) || strings.Contains(want, c.Product) {
			n += 10000
		}
		if preferTarget != "" && c.TargetSW == preferTarget {
			n += 50000
		}
		if vendor != "" && c.Vendor == normalizeCPE(vendor) {
			n += 200000
		}
		return n
	}
	ranked := append([]cpeProduct(nil), cands...)
	sort.SliceStable(ranked, func(i, j int) bool { return score(ranked[i]) > score(ranked[j]) })
	if len(ranked) == 0 {
		return nil, nil
	}
	best := ranked[0]
	if best.Product != want && !strings.Contains(best.Product, want) && !strings.Contains(want, best.Product) {
		return nil, nil // keyword hit on an unrelated product; don't guess
	}
	if best.Product != want || vendor != "" {
		return []cpeProduct{best}, nil
	}
	var out []cpeProduct
	for _, c := range ranked {
		if c.Product == want && (preferTarget == "" || c.TargetSW == best.TargetSW) && len(out) < maxCPEProducts {
			out = append(out, c)
		}
	}
	return out, nil
}

// nvdByProduct fetches every CVE whose applicability names part:vendor:product.
func (s *Service) nvdByProduct(ctx context.Context, cfg Settings, p cpeProduct) ([]nvdCVE, error) {
	match := fmt.Sprintf("cpe:2.3:%s:%s:%s", p.Part, p.Vendor, p.Product)
	s.mu.Lock()
	ce, ok := s.nvdCache[match]
	s.mu.Unlock()
	if ok && ce.fresh(nvdCacheTTL) {
		return ce.val, nil
	}
	var out []nvdCVE
	for page := 0; page < nvdMaxPages; page++ {
		q := url.Values{
			"virtualMatchString": {match},
			"resultsPerPage":     {fmt.Sprint(nvdPageSize)},
			"startIndex":         {fmt.Sprint(page * nvdPageSize)},
		}
		var resp nvdResponse
		if err := s.nvdGet(ctx, cfg, "/cves/2.0", q, &resp); err != nil {
			return nil, err
		}
		for _, v := range resp.Vulnerabilities {
			out = append(out, parseNVD(v.CVE))
		}
		if (page+1)*nvdPageSize >= resp.TotalResults {
			break
		}
	}
	s.mu.Lock()
	s.nvdCache[match] = cacheEntry[[]nvdCVE]{out, time.Now()}
	s.mu.Unlock()
	return out, nil
}

// nvdLookup returns the CVEs affecting product version (all when version is empty).
func (s *Service) nvdLookup(ctx context.Context, cfg Settings, p cpeProduct, version string) ([]Vuln, error) {
	cves, err := s.nvdByProduct(ctx, cfg, p)
	if err != nil {
		return nil, err
	}
	var out []Vuln
	for _, c := range cves {
		if c.Status == "Rejected" {
			continue
		}
		var labels []string
		hit := false
		for _, m := range c.Matches {
			if m.Vendor != p.Vendor || m.Product != p.Product {
				continue
			}
			labels = append(labels, m.label())
			if version == "" || m.contains(version) {
				hit = true
			}
		}
		if !hit {
			continue
		}
		v := c.vuln()
		v.Affected = uniq(labels)
		out = append(out, v)
	}
	return out, nil
}

// NVDDetail fetches one CVE record by id.
func (s *Service) nvdDetail(ctx context.Context, cfg Settings, id string) (*nvdCVE, error) {
	var resp nvdResponse
	if err := s.nvdGet(ctx, cfg, "/cves/2.0", url.Values{"cveId": {id}}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Vulnerabilities) == 0 {
		return nil, nil
	}
	c := parseNVD(resp.Vulnerabilities[0].CVE)
	return &c, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func dateOnly(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}
