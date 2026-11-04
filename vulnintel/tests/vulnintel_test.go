//go:build ignore

package vulnintel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"6.9.4", "6.9.4", 0},
		{"6.9.4", "6.9.10", -1},
		{"7.1", "7.1.0", 0},
		{"7.1.1", "7.1", 1},
		{"1.0.0-rc1", "1.0.0", -1},
		{"2.4beta", "2.4", -1},
		{"v1.18.0", "1.18.0", 0},
		{"10.0", "9.9.9", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.2.3.final", "1.2.3", 0},
	}
	for _, c := range cases {
		got := CompareVersions(c.a, c.b)
		if (got < 0) != (c.want < 0) || (got > 0) != (c.want > 0) {
			t.Errorf("CompareVersions(%q,%q)=%d want sign %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRangeContains(t *testing.T) {
	r := Range{StartIncl: "4.7", EndExcl: "7.1.4"}
	for v, want := range map[string]bool{"4.7": true, "6.9.4": true, "7.1.3": true, "7.1.4": false, "4.6.9": false} {
		if r.Contains(v) != want {
			t.Errorf("%s in %s = %v, want %v", v, r, !want, want)
		}
	}
	if got := (Range{}).String(); got != "all versions" {
		t.Errorf("empty range label %q", got)
	}
}

// fakeNVD serves the CPE and CVE endpoints with a fixed nginx-like dataset.
func fakeNVD(t *testing.T, calls *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		q := r.URL.Query()
		switch r.URL.Path {
		case "/cpes/2.0":
			if !strings.HasPrefix(q.Get("cpeMatchString"), "cpe:2.3:*:*:nginx") {
				_, _ = w.Write([]byte(`{"products":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"products":[
				{"cpe":{"cpeName":"cpe:2.3:a:f5:nginx:1.20.0:*:*:*:*:*:*:*"}},
				{"cpe":{"cpeName":"cpe:2.3:a:igor_sysoev:nginx:0.5.0:*:*:*:*:*:*:*"}}]}`))
		case "/cves/2.0":
			if q.Get("cveId") != "" {
				_, _ = w.Write([]byte(`{"totalResults":0,"vulnerabilities":[]}`))
				return
			}
			match := q.Get("virtualMatchString")
			if match == "cpe:2.3:a:f5:nginx" {
				_, _ = w.Write([]byte(`{"totalResults":2,"vulnerabilities":[
				{"cve":{"id":"CVE-2021-23017","published":"2021-06-01T00:00:00","vulnStatus":"Analyzed",
				 "descriptions":[{"lang":"en","value":"resolver off-by-one"}],
				 "metrics":{"cvssMetricV31":[{"cvssData":{"baseScore":7.7,"baseSeverity":"HIGH","vectorString":"CVSS:3.1/AV:N"}}]},
				 "weaknesses":[{"description":[{"value":"CWE-193"}]}],
				 "configurations":[{"nodes":[{"cpeMatch":[{"vulnerable":true,"criteria":"cpe:2.3:a:f5:nginx:*:*:*:*:*:*:*:*","versionStartIncluding":"0.6.18","versionEndExcluding":"1.20.1"}]}]}],
				 "references":[{"url":"https://example.org/poc","tags":["Exploit"]},{"url":"https://example.org/advisory","tags":["Vendor Advisory"]}]}},
				{"cve":{"id":"CVE-2099-0001","vulnStatus":"Analyzed","descriptions":[{"lang":"en","value":"newer only"}],
				 "configurations":[{"nodes":[{"cpeMatch":[{"vulnerable":true,"criteria":"cpe:2.3:a:f5:nginx:*:*:*:*:*:*:*:*","versionStartIncluding":"1.25.0"}]}]}]}}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"totalResults":0,"vulnerabilities":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func fakeFeeds(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cisa"):
			_, _ = w.Write([]byte(`{"vulnerabilities":[{"cveID":"CVE-2021-23017","vendorProject":"F5","product":"NGINX","vulnerabilityName":"NGINX resolver","dateAdded":"2024-01-02","dueDate":"2024-01-23","knownRansomwareCampaignUse":"Known","cwes":["CWE-193"]},
				{"cveID":"CVE-2000-1111","vendorProject":"F5","product":"NGINX","vulnerabilityName":"old one","dateAdded":"2020-01-01"}]}`))
		case strings.HasSuffix(r.URL.Path, "/eu"):
			_, _ = w.Write([]byte(`[{"cveID":"CVE-2021-23017","euvdID":"EUVD-1","vendorProject":"F5","product":"nginx","dateReported":"2026/08/10","exploitationType":"APT","cwes":"CWE-193, CWE-20"}]`))
		case strings.HasSuffix(r.URL.Path, "/epss"):
			_, _ = w.Write([]byte(`{"data":[{"cve":"CVE-2021-23017","epss":"0.5","percentile":"0.9","date":"2026-11-04"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func newTestService(t *testing.T, cfg Settings) (*Service, *int32) {
	t.Helper()
	var calls int32
	nvd := fakeNVD(t, &calls)
	feeds := fakeFeeds(t)
	t.Cleanup(nvd.Close)
	t.Cleanup(feeds.Close)
	if cfg.Keys == nil {
		cfg.Keys = map[string]string{SourceNVD: "test"} // keeps the rate gate short
	}
	s := New(func() Settings { return cfg }, t.TempDir())
	s.BaseURL = map[string]string{
		SourceNVD: nvd.URL, SourceCISAKEV: feeds.URL + "/cisa", SourceEUKEV: feeds.URL + "/eu", SourceEPSS: feeds.URL + "/epss",
	}
	return s, &calls
}

func TestLookupMatchesRangesAndEnriches(t *testing.T) {
	s, _ := newTestService(t, Settings{})
	res := s.Lookup(context.Background(), []Component{{Kind: "product", Name: "nginx", Version: "1.18.0"}})
	if len(res) != 1 {
		t.Fatalf("results %d", len(res))
	}
	r := res[0]
	if !r.Complete {
		t.Fatalf("expected complete result, sources %+v", r.Sources)
	}
	if len(r.Vulns) != 1 || r.Vulns[0].ID != "CVE-2021-23017" {
		t.Fatalf("vulns %+v", r.Vulns)
	}
	v := r.Vulns[0]
	if v.CVSS != 7.7 || v.Severity != "HIGH" || len(v.ExploitRefs) != 1 {
		t.Errorf("parsed fields %+v", v)
	}
	if len(v.KEV) != 2 || v.KEV[0].Ransomware != "Known" || v.KEV[1].Catalog != SourceEUKEV {
		t.Errorf("kev marks %+v", v.KEV)
	}
	if v.EPSS == nil || v.EPSS.Score != 0.5 {
		t.Errorf("epss %+v", v.EPSS)
	}
	// The second catalog row names the product but is not version-matched.
	if len(r.KEVProductMatches) != 1 || r.KEVProductMatches[0].CVE != "CVE-2000-1111" {
		t.Errorf("kev product matches %+v", r.KEVProductMatches)
	}
	var vc SourceStatus
	for _, st := range r.Sources {
		if st.Source == SourceVulnCheckKEV {
			vc = st
		}
	}
	if vc.Status != "skipped" {
		t.Errorf("vulncheck without key should be skipped, got %+v", vc)
	}
}

func TestLookupCachesNVD(t *testing.T) {
	s, calls := newTestService(t, Settings{})
	c := []Component{{Kind: "product", Name: "nginx", Version: "1.18.0"}}
	s.Lookup(context.Background(), c)
	first := atomic.LoadInt32(calls)
	s.Lookup(context.Background(), c)
	if atomic.LoadInt32(calls) != first {
		t.Errorf("second lookup hit the network again: %d -> %d", first, atomic.LoadInt32(calls))
	}
}

func TestDisabledSourcesMakeResultIncomplete(t *testing.T) {
	s, _ := newTestService(t, Settings{Enabled: map[string]bool{SourceNVD: false}})
	r := s.Lookup(context.Background(), []Component{{Kind: "product", Name: "nginx", Version: "1.18.0"}})[0]
	if r.Complete || len(r.Vulns) != 0 {
		t.Fatalf("nothing answered, must not be complete: %+v", r)
	}
	if !strings.Contains(r.Note, "NOT evidence") {
		t.Errorf("note should warn about missing coverage: %q", r.Note)
	}
}

func TestSourceErrorIsReported(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer broken.Close()
	s := New(func() Settings { return Settings{Keys: map[string]string{SourceNVD: "k"}} }, t.TempDir())
	s.BaseURL = map[string]string{SourceNVD: broken.URL}
	r := s.Lookup(context.Background(), []Component{{Kind: "product", Name: "nginx", Version: "1.0"}})[0]
	if r.Complete {
		t.Fatal("an erroring source must make the result incomplete")
	}
	if r.Sources[0].Status != "error" || !strings.Contains(r.Sources[0].Detail, "503") {
		t.Errorf("status %+v", r.Sources[0])
	}
}

func TestOSVPackageLookup(t *testing.T) {
	osv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Package struct{ Name, Ecosystem string }
			Version string
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		if q.Package.Ecosystem != "npm" || q.Version != "1.12.4" {
			t.Errorf("query %+v", q)
		}
		_, _ = w.Write([]byte(`{"vulns":[{"id":"GHSA-x","aliases":["CVE-2019-11358"],"summary":"proto pollution",
			"affected":[{"package":{"name":"jquery","ecosystem":"npm"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.1.4"},{"fixed":"3.4.0"}]}]}],
			"database_specific":{"severity":"MODERATE","cwe_ids":["CWE-1321"]}}]}`))
	}))
	defer osv.Close()
	s := New(func() Settings {
		return Settings{Enabled: map[string]bool{SourceCISAKEV: false, SourceEUKEV: false, SourceEPSS: false}}
	}, t.TempDir())
	s.BaseURL = map[string]string{SourceOSV: osv.URL}
	r := s.Lookup(context.Background(), []Component{{Kind: "package", Name: "jquery", Ecosystem: "node", Version: "1.12.4"}})[0]
	if len(r.Vulns) != 1 {
		t.Fatalf("vulns %+v sources %+v", r.Vulns, r.Sources)
	}
	v := r.Vulns[0]
	if v.ID != "CVE-2019-11358" || v.FixedIn != "3.4.0" || v.Severity != "MEDIUM" || v.Affected[0] != ">=1.1.4 <3.4.0" {
		t.Errorf("osv mapping %+v", v)
	}
}

func TestMergerJoinsSourcesByCVE(t *testing.T) {
	m := newMerger()
	m.add(Vuln{ID: "CVE-2024-1", CVSS: 5, Sources: []string{SourceNVD}})
	m.add(Vuln{ID: "WF-abc", Aliases: []string{"CVE-2024-1"}, CVSS: 9.1, Severity: "CRITICAL", FixedIn: "2.0", Sources: []string{SourceWordfence}})
	m.add(Vuln{ID: "WPSCAN-9", Sources: []string{SourceWPScan}})
	got := m.list()
	if len(got) != 2 {
		t.Fatalf("merged %+v", got)
	}
	if got[0].CVSS != 9.1 || got[0].FixedIn != "2.0" || len(got[0].Sources) != 2 {
		t.Errorf("merge result %+v", got[0])
	}
}

func TestParseWordfenceAndMatch(t *testing.T) {
	feed := `{"a1":{"id":"a1","title":"RevSlider file upload","software":[{"type":"plugin","slug":"revslider",
		"affected_versions":{"* - 6.6.12":{"from_version":"*","from_inclusive":true,"to_version":"6.6.12","to_inclusive":true}},
		"patched":true,"patched_versions":["6.6.13"]}],"cve":"CVE-2023-2359","cvss":{"vector":"CVSS:3.1/AV:N","score":8.8,"rating":"High"},
		"cwe":{"id":434,"name":"Unrestricted Upload"},"published":"2023-06-01T00:00:00"}}`
	idx, err := parseWordfence(strings.NewReader(feed))
	if err != nil {
		t.Fatal(err)
	}
	recs := idx.bySoftware["plugin|revslider"]
	if len(recs) != 1 || !recs[0].ranges[0].Contains("6.6.7") || recs[0].ranges[0].Contains("6.6.13") {
		t.Fatalf("records %+v", recs)
	}
	if v := recs[0].vuln; v.ID != "CVE-2023-2359" || v.Severity != "HIGH" || v.FixedIn != "6.6.13" || v.CWE[0] != "CWE-434" {
		t.Errorf("vuln %+v", v)
	}
}

func TestKEVSearchFilters(t *testing.T) {
	s, _ := newTestService(t, Settings{})
	res := s.KEVSearch(context.Background(), KEVQuery{Query: "nginx", Ransomware: true})
	if res.Total != 1 || res.Entries[0].CVE != "CVE-2021-23017" {
		t.Fatalf("search %+v", res)
	}
	res = s.KEVSearch(context.Background(), KEVQuery{Vendor: "f5", Since: "2026-01-01"})
	if res.Total != 1 || res.Entries[0].Catalog != SourceEUKEV || res.Entries[0].DateAdded != "2026-08-10" {
		t.Fatalf("since filter %+v", res)
	}
}

func TestRequiredKeyIsSkipped(t *testing.T) {
	cfg := Settings{}
	if ok, why := cfg.usable(SourceWordfence); ok || why != "no API key configured" {
		t.Errorf("wordfence without key: %v %q", ok, why)
	}
	cfg.Keys = map[string]string{SourceWordfence: "k"}
	if ok, _ := cfg.usable(SourceWordfence); !ok {
		t.Error("wordfence with key should be usable")
	}
	cfg.Enabled = map[string]bool{SourceWordfence: false}
	if ok, why := cfg.usable(SourceWordfence); ok || why != "disabled in settings" {
		t.Errorf("disabled wordfence: %v %q", ok, why)
	}
}
