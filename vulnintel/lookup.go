package vulnintel

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// MaxComponents bounds one Lookup call.
const MaxComponents = 20

var componentKinds = map[string]bool{
	"wordpress_core": true, "wordpress_plugin": true, "wordpress_theme": true, "product": true, "package": true,
}

// Lookup answers which known vulnerabilities affect each component.
func (s *Service) Lookup(ctx context.Context, comps []Component) []ComponentResult {
	cfg := s.settings()
	if len(comps) > MaxComponents {
		comps = comps[:MaxComponents]
	}
	out := make([]ComponentResult, 0, len(comps))
	for _, c := range comps {
		out = append(out, s.lookupOne(ctx, cfg, c))
	}
	return out
}

func (s *Service) lookupOne(ctx context.Context, cfg Settings, c Component) ComponentResult {
	c.Kind = strings.ToLower(strings.TrimSpace(c.Kind))
	if c.Kind == "" {
		c.Kind = "product"
	}
	c.Name, c.Version = strings.TrimSpace(c.Name), strings.TrimSpace(c.Version)
	res := ComponentResult{Component: c}
	if !componentKinds[c.Kind] {
		res.Note = "unknown kind; use wordpress_core, wordpress_plugin, wordpress_theme, product or package"
		return res
	}
	if c.Name == "" && c.Kind != "wordpress_core" {
		res.Note = "name is required"
		return res
	}
	m := newMerger()
	status := func(st SourceStatus) { res.Sources = append(res.Sources, st) }
	collect := func(src string, vs []Vuln, stale bool, err error) {
		switch {
		case err != nil:
			status(SourceStatus{Source: src, Status: "error", Detail: err.Error()})
		case stale:
			status(SourceStatus{Source: src, Status: "stale", Detail: "refresh failed; using the cached copy", Count: len(vs)})
		default:
			status(SourceStatus{Source: src, Status: "ok", Count: len(vs)})
		}
		for _, v := range vs {
			m.add(v)
		}
	}
	// try runs a vulnerability source if it is usable, recording why when it is not.
	try := func(src string, run func() ([]Vuln, bool, error)) {
		if ok, why := cfg.usable(src); !ok {
			status(SourceStatus{Source: src, Status: "skipped", Detail: why})
			return
		}
		vs, stale, err := run()
		collect(src, vs, stale, err)
	}
	notApplicable := func(src, why string) { status(SourceStatus{Source: src, Status: "not_applicable", Detail: why}) }

	switch c.Kind {
	case "wordpress_core", "wordpress_plugin", "wordpress_theme":
		try(SourceWordfence, func() ([]Vuln, bool, error) { return s.wordfenceLookup(ctx, cfg, c.Kind, c.Name, c.Version) })
		try(SourceWPScan, func() ([]Vuln, bool, error) {
			vs, err := s.wpscanLookup(ctx, cfg, c.Kind, c.Name, c.Version)
			return vs, false, err
		})
		s.nvdComponent(ctx, cfg, c, &res, collect, status)
		notApplicable(SourceOSV, "OSV has no WordPress ecosystem")
	case "product":
		s.nvdComponent(ctx, cfg, c, &res, collect, status)
		if c.Ecosystem != "" {
			try(SourceOSV, func() ([]Vuln, bool, error) {
				vs, err := s.osvLookup(ctx, cfg, c.Name, c.Ecosystem, c.Version)
				return vs, false, err
			})
		} else {
			notApplicable(SourceOSV, "no ecosystem given (set ecosystem to query OSV)")
		}
	case "package":
		if c.Ecosystem == "" {
			status(SourceStatus{Source: SourceOSV, Status: "error", Detail: "ecosystem is required for kind=package (npm, PyPI, Packagist, Maven, Go, …)"})
		} else {
			try(SourceOSV, func() ([]Vuln, bool, error) {
				vs, err := s.osvLookup(ctx, cfg, c.Name, c.Ecosystem, c.Version)
				return vs, false, err
			})
		}
		notApplicable(SourceNVD, "packages are matched through OSV")
	}

	res.Vulns = m.list()
	s.enrich(ctx, cfg, res.Vulns, status)
	res.KEVProductMatches = s.productKEV(ctx, cfg, c, res.Vulns)
	sortVulns(res.Vulns)

	res.Complete = true
	answered := 0
	for _, st := range res.Sources {
		if !isVulnSource(st.Source) {
			continue
		}
		switch st.Status {
		case "ok", "stale":
			answered++
		case "error":
			res.Complete = false
		}
	}
	if answered == 0 {
		res.Complete = false
	}
	switch {
	case answered == 0:
		res.Note = "no vulnerability source could answer for this component; this is NOT evidence that it is unaffected"
	case !res.Complete:
		res.Note = "some sources failed; an empty or short list is not proof that the component is unaffected"
	case c.Version == "":
		res.Note = "no version given: every known vulnerability of the component is listed"
	case len(res.Vulns) == 0:
		res.Note = "no known vulnerability for this version in the answering sources"
	}
	return res
}

func isVulnSource(src string) bool {
	switch src {
	case SourceNVD, SourceOSV, SourceWordfence, SourceWPScan:
		return true
	}
	return false
}

// nvdComponent resolves the component to a CPE product and matches NVD ranges.
func (s *Service) nvdComponent(ctx context.Context, cfg Settings, c Component, res *ComponentResult,
	collect func(string, []Vuln, bool, error), status func(SourceStatus)) {
	if ok, why := cfg.usable(SourceNVD); !ok {
		status(SourceStatus{Source: SourceNVD, Status: "skipped", Detail: why})
		return
	}
	var products []cpeProduct
	var err error
	switch c.Kind {
	case "wordpress_core":
		products = []cpeProduct{{Part: "a", Vendor: "wordpress", Product: "wordpress"}}
	case "wordpress_plugin", "wordpress_theme":
		products, err = s.resolveCPE(ctx, cfg, strings.ReplaceAll(c.Name, "-", " "), c.Vendor, "wordpress")
	default:
		products, err = s.resolveCPE(ctx, cfg, c.Name, c.Vendor, "")
	}
	if err != nil {
		collect(SourceNVD, nil, false, err)
		return
	}
	if len(products) == 0 {
		status(SourceStatus{Source: SourceNVD, Status: "not_applicable",
			Detail: "no matching CPE product in the NVD dictionary; retry with the product's display name or a vendor"})
		return
	}
	var all []Vuln
	var cpes []string
	for _, p := range products {
		vs, err := s.nvdLookup(ctx, cfg, p, c.Version)
		if err != nil {
			collect(SourceNVD, all, false, err)
			return
		}
		all = append(all, vs...)
		cpes = append(cpes, fmt.Sprintf("cpe:2.3:%s:%s:%s", p.Part, p.Vendor, p.Product))
	}
	collect(SourceNVD, all, false, nil)
	res.Sources[len(res.Sources)-1].Detail = "matched " + strings.Join(cpes, ", ")
}

// enrich adds KEV listings and EPSS scores to every CVE in vs.
func (s *Service) enrich(ctx context.Context, cfg Settings, vs []Vuln, status func(SourceStatus)) {
	ids := cveIDs(vs)
	if len(ids) == 0 {
		return
	}
	at := map[string]int{}
	for i, v := range vs {
		at[v.ID] = i
	}
	for _, src := range []string{SourceCISAKEV, SourceEUKEV} {
		if ok, why := cfg.usable(src); !ok {
			status(SourceStatus{Source: src, Status: "skipped", Detail: why})
			continue
		}
		ks, err := s.kevCatalog(ctx, cfg, src)
		if err != nil {
			status(SourceStatus{Source: src, Status: "error", Detail: err.Error()})
			continue
		}
		n := 0
		for _, id := range ids {
			for _, e := range ks.lookup(id) {
				vs[at[id]].KEV = append(vs[at[id]].KEV, e.mark())
				n++
			}
		}
		st := SourceStatus{Source: src, Status: "ok", Count: n}
		if ks.stale {
			st.Status, st.Detail = "stale", "refresh failed; using the cached catalog"
		}
		status(st)
	}
	if ok, why := cfg.usable(SourceVulnCheckKEV); !ok {
		status(SourceStatus{Source: SourceVulnCheckKEV, Status: "skipped", Detail: why})
	} else {
		n, checked := 0, 0
		var lastErr error
		for _, id := range ids {
			if checked >= vulnCheckCap {
				break
			}
			checked++
			mk, err := s.vulnCheckKEV(ctx, cfg, id)
			if err != nil {
				lastErr = err
				break
			}
			if mk != nil {
				v := &vs[at[id]]
				v.KEV = append(v.KEV, *mk)
				v.ExploitRefs = uniq(append(v.ExploitRefs, mk.ExploitReferences...))
				n++
			}
		}
		switch {
		case lastErr != nil:
			status(SourceStatus{Source: SourceVulnCheckKEV, Status: "error", Detail: lastErr.Error(), Count: n})
		case checked < len(ids):
			status(SourceStatus{Source: SourceVulnCheckKEV, Status: "ok", Count: n,
				Detail: fmt.Sprintf("checked the first %d of %d CVEs", checked, len(ids))})
		default:
			status(SourceStatus{Source: SourceVulnCheckKEV, Status: "ok", Count: n})
		}
	}
	if ok, why := cfg.usable(SourceEPSS); !ok {
		status(SourceStatus{Source: SourceEPSS, Status: "skipped", Detail: why})
		return
	}
	scores, err := s.epss(ctx, cfg, ids)
	for id, sc := range scores {
		if i, ok := at[id]; ok {
			vs[i].EPSS = sc
		}
	}
	if err != nil {
		status(SourceStatus{Source: SourceEPSS, Status: "error", Detail: err.Error(), Count: len(scores)})
		return
	}
	status(SourceStatus{Source: SourceEPSS, Status: "ok", Count: len(scores)})
}

// productKEV lists KEV rows that name the component but are not among the
// version-matched results, so exploited issues are never silently dropped
// just because a range could not be confirmed.
func (s *Service) productKEV(ctx context.Context, cfg Settings, c Component, vs []Vuln) []KEVEntry {
	names := []string{c.Name}
	if c.Kind == "wordpress_core" {
		names = []string{"wordpress core"}
	}
	known := map[string]bool{}
	for _, v := range vs {
		known[v.ID] = true
	}
	var out []KEVEntry
	for _, src := range []string{SourceCISAKEV, SourceEUKEV} {
		if ok, _ := cfg.usable(src); !ok {
			continue
		}
		ks, err := s.kevCatalog(ctx, cfg, src)
		if err != nil {
			continue
		}
		for _, e := range kevProductMatches(ks, names...) {
			if !known[strings.ToUpper(e.CVE)] {
				out = append(out, e)
			}
		}
	}
	return capKEV(out, 15)
}

// CVEDetail is the cve_lookup answer for one id.
type CVEDetail struct {
	ID          string         `json:"id"`
	Found       bool           `json:"found"`
	Description string         `json:"description,omitempty"`
	Aliases     []string       `json:"aliases,omitempty"`
	Severity    string         `json:"severity,omitempty"`
	CVSS        float64        `json:"cvss,omitempty"`
	Vector      string         `json:"cvss_vector,omitempty"`
	CWE         []string       `json:"cwe,omitempty"`
	Published   string         `json:"published,omitempty"`
	Status      string         `json:"nvd_status,omitempty"`
	Affected    []string       `json:"affected_products,omitempty"`
	KEV         []KEVMark      `json:"kev,omitempty"`
	EPSS        *EPSS          `json:"epss,omitempty"`
	ExploitRefs []string       `json:"exploit_refs,omitempty"`
	References  []string       `json:"references,omitempty"`
	Sources     []SourceStatus `json:"sources"`
}

// MaxCVEs bounds one CVEDetails call.
const MaxCVEs = 20

// CVEDetails resolves vulnerability ids (CVE, GHSA, …) across all enabled sources.
func (s *Service) CVEDetails(ctx context.Context, ids []string) []CVEDetail {
	cfg := s.settings()
	ids = uniq(ids)
	if len(ids) > MaxCVEs {
		ids = ids[:MaxCVEs]
	}
	out := make([]CVEDetail, 0, len(ids))
	for _, raw := range ids {
		id := strings.ToUpper(strings.TrimSpace(raw))
		d := CVEDetail{ID: id}
		status := func(st SourceStatus) { d.Sources = append(d.Sources, st) }
		if strings.HasPrefix(id, "CVE-") {
			if ok, why := cfg.usable(SourceNVD); !ok {
				status(SourceStatus{Source: SourceNVD, Status: "skipped", Detail: why})
			} else if c, err := s.nvdDetail(ctx, cfg, id); err != nil {
				status(SourceStatus{Source: SourceNVD, Status: "error", Detail: err.Error()})
			} else if c == nil {
				status(SourceStatus{Source: SourceNVD, Status: "ok", Detail: "not in NVD"})
			} else {
				d.Found = true
				d.Description, d.CVSS, d.Severity, d.Vector = c.Description, c.Score, c.Severity, c.Vector
				d.CWE, d.Published, d.Status = c.CWE, dateOnly(c.Published), c.Status
				d.ExploitRefs, d.References = capList(c.Exploits, 8), capList(c.Refs, 8)
				var aff []string
				for _, mt := range c.Matches {
					aff = append(aff, fmt.Sprintf("%s:%s %s", mt.Vendor, mt.Product, mt.label()))
				}
				d.Affected = capList(uniq(aff), 15)
				status(SourceStatus{Source: SourceNVD, Status: "ok", Count: 1})
			}
		} else {
			status(SourceStatus{Source: SourceNVD, Status: "not_applicable", Detail: "not a CVE id"})
		}
		if ok, why := cfg.usable(SourceOSV); !ok {
			status(SourceStatus{Source: SourceOSV, Status: "skipped", Detail: why})
		} else if o, err := s.osvByID(ctx, cfg, id); err != nil {
			status(SourceStatus{Source: SourceOSV, Status: "error", Detail: err.Error()})
		} else if o == nil {
			status(SourceStatus{Source: SourceOSV, Status: "ok", Detail: "not in OSV"})
		} else {
			v := o.vuln("")
			d.Aliases = uniq(append(d.Aliases, append(o.Aliases, o.ID)...), id)
			if !d.Found {
				d.Found = true
				d.Description, d.Severity, d.Vector, d.CWE, d.Published = v.Title, v.Severity, v.Vector, v.CWE, v.Published
				d.References = v.References
			}
			for _, a := range o.Affected {
				d.Affected = append(d.Affected, a.Package.Ecosystem+":"+a.Package.Name)
			}
			d.Affected = capList(uniq(d.Affected), 15)
			d.ExploitRefs = uniq(append(d.ExploitRefs, v.ExploitRefs...))
			status(SourceStatus{Source: SourceOSV, Status: "ok", Count: 1})
		}
		cve := id
		if !strings.HasPrefix(cve, "CVE-") {
			for _, a := range d.Aliases {
				if strings.HasPrefix(strings.ToUpper(a), "CVE-") {
					cve = strings.ToUpper(a)
					break
				}
			}
		}
		if strings.HasPrefix(cve, "CVE-") {
			one := []Vuln{{ID: cve}}
			s.enrich(ctx, cfg, one, status)
			d.KEV, d.EPSS = one[0].KEV, one[0].EPSS
			d.ExploitRefs = capList(uniq(append(d.ExploitRefs, one[0].ExploitRefs...)), 10)
			if len(d.KEV) > 0 {
				d.Found = true
			}
		}
		out = append(out, d)
	}
	return out
}

// TestSource checks that one source answers with the current settings; key, when
// non-empty, overrides the stored key so the UI can test before saving.
func (s *Service) TestSource(ctx context.Context, source, key string) (string, error) {
	cfg := s.settings()
	if key = strings.TrimSpace(key); key != "" {
		keys := map[string]string{}
		for k, v := range cfg.Keys {
			keys[k] = v
		}
		keys[source] = key
		cfg.Keys = keys
	}
	cfg.Enabled = map[string]bool{source: true}
	if info, ok := SourceByID(source); !ok {
		return "", fmt.Errorf("unknown source %q", source)
	} else if info.Key == "required" && cfg.key(source) == "" {
		return "", fmt.Errorf("an API key is required")
	}
	switch source {
	case SourceNVD:
		c, err := s.nvdDetail(ctx, cfg, "CVE-2021-44228")
		if err != nil {
			return "", err
		}
		if c == nil {
			return "", fmt.Errorf("unexpected empty answer")
		}
		return "CVE-2021-44228 retrieved", nil
	case SourceOSV:
		o, err := s.osvByID(ctx, cfg, "GHSA-jfh8-c2jp-5v3q")
		if err != nil {
			return "", err
		}
		if o == nil {
			return "", fmt.Errorf("unexpected empty answer")
		}
		return "GHSA-jfh8-c2jp-5v3q retrieved", nil
	case SourceCISAKEV, SourceEUKEV:
		ks, err := s.kevCatalog(ctx, cfg, source)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d catalog entries loaded", len(ks.entries)), nil
	case SourceVulnCheckKEV:
		s.mu.Lock()
		delete(s.vcCache, "CVE-2021-44228")
		s.mu.Unlock()
		mk, err := s.vulnCheckKEV(ctx, cfg, "CVE-2021-44228")
		if err != nil {
			return "", err
		}
		if mk == nil {
			return "", fmt.Errorf("CVE-2021-44228 not returned; check the key's index access")
		}
		return "CVE-2021-44228 found in VulnCheck KEV", nil
	case SourceEPSS:
		sc, err := s.epss(ctx, cfg, []string{"CVE-2021-44228"})
		if err != nil {
			return "", err
		}
		if sc["CVE-2021-44228"] == nil {
			return "", fmt.Errorf("unexpected empty answer")
		}
		return fmt.Sprintf("EPSS for CVE-2021-44228: %.3f", sc["CVE-2021-44228"].Score), nil
	case SourceWordfence:
		s.mu.Lock()
		s.wordfence = nil
		s.mu.Unlock()
		idx, err := s.loadWordfence(ctx, cfg)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("feed loaded: %d software entries", len(idx.bySoftware)), nil
	case SourceWPScan:
		var st struct {
			Plan              string `json:"plan"`
			RequestsRemaining any    `json:"requests_remaining"`
		}
		header := map[string]string{"Authorization": "Token token=" + cfg.key(SourceWPScan)}
		if err := s.getJSON(ctx, cfg, http.MethodGet, s.base(SourceWPScan, wpscanURL)+"/status", header, nil, &st); err != nil {
			return "", err
		}
		return fmt.Sprintf("plan %s, requests remaining today: %v", st.Plan, st.RequestsRemaining), nil
	}
	return "", fmt.Errorf("unknown source %q", source)
}
