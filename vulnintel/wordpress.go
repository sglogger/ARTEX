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
	"strings"
	"time"
)

const (
	wordfenceURL = "https://www.wordfence.com/api/intelligence/v3/vulnerabilities/production"
	wpscanURL    = "https://wpscan.com/api/v3"
	wordfenceTTL = 24 * time.Hour
)

// ---- Wordfence Intelligence (feed v3) ----

type wfWire struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Software []struct {
		Type             string `json:"type"` // core | plugin | theme
		Slug             string `json:"slug"`
		AffectedVersions map[string]struct {
			FromVersion   string `json:"from_version"`
			FromInclusive bool   `json:"from_inclusive"`
			ToVersion     string `json:"to_version"`
			ToInclusive   bool   `json:"to_inclusive"`
		} `json:"affected_versions"`
		Patched         bool     `json:"patched"`
		PatchedVersions []string `json:"patched_versions"`
	} `json:"software"`
	References []string `json:"references"`
	CVE        string   `json:"cve"`
	CVSS       *struct {
		Vector string  `json:"vector"`
		Score  float64 `json:"score"`
		Rating string  `json:"rating"`
	} `json:"cvss"`
	CWE *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"cwe"`
	Published string `json:"published"`
}

type wfRecord struct {
	vuln   Vuln
	ranges []Range
}

type wordfenceIndex struct {
	bySoftware map[string][]wfRecord // "type|slug"
	stale      bool
}

func wpType(kind string) string {
	switch kind {
	case "wordpress_core":
		return "core"
	case "wordpress_theme":
		return "theme"
	}
	return "plugin"
}

// loadWordfence downloads the feed to disk at most once per TTL and streams it
// into an index, so the multi-megabyte document is never held in memory twice.
func (s *Service) loadWordfence(ctx context.Context, cfg Settings) (*wordfenceIndex, error) {
	s.mu.Lock()
	idx, loadedAt := s.wordfence, s.wfLoaded
	s.mu.Unlock()
	if idx != nil && time.Since(loadedAt) < wordfenceTTL && !isLive(ctx) {
		return idx, nil
	}
	dir := s.cacheDir
	if dir == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, "wordfence.json")
	live := isLive(ctx)
	fresh := false
	if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < wordfenceTTL && !live {
		fresh = true
	}
	stale := false
	if !fresh {
		if err := s.downloadTo(ctx, cfg, s.base(SourceWordfence, wordfenceURL), map[string]string{"Authorization": "Bearer " + cfg.key(SourceWordfence)}, path); err != nil {
			if _, statErr := os.Stat(path); statErr != nil || live {
				return nil, err
			}
			stale = true // keep using the previous download
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	idx, err = parseWordfence(f)
	if err != nil {
		return nil, err
	}
	idx.stale = stale
	s.mu.Lock()
	s.wordfence, s.wfLoaded = idx, time.Now()
	s.mu.Unlock()
	return idx, nil
}

func parseWordfence(r io.Reader) (*wordfenceIndex, error) {
	dec := json.NewDecoder(r)
	if _, err := dec.Token(); err != nil { // opening {
		return nil, fmt.Errorf("parse Wordfence feed: %w", err)
	}
	idx := &wordfenceIndex{bySoftware: map[string][]wfRecord{}}
	for dec.More() {
		if _, err := dec.Token(); err != nil { // record id key
			return nil, fmt.Errorf("parse Wordfence feed: %w", err)
		}
		var w wfWire
		if err := dec.Decode(&w); err != nil {
			return nil, fmt.Errorf("parse Wordfence feed: %w", err)
		}
		base := Vuln{ID: firstNonEmpty(w.CVE, "WF-"+w.ID), Title: w.Title, Published: dateOnly(w.Published),
			Sources: []string{SourceWordfence}, References: capList(w.References, 5)}
		if w.CVE != "" {
			base.Aliases = []string{"WF-" + w.ID}
		}
		if w.CVSS != nil {
			base.CVSS, base.Vector, base.Severity = w.CVSS.Score, w.CVSS.Vector, strings.ToUpper(w.CVSS.Rating)
		}
		if w.CWE != nil && w.CWE.ID > 0 {
			base.CWE = []string{fmt.Sprintf("CWE-%d", w.CWE.ID)}
		}
		for _, sw := range w.Software {
			rec := wfRecord{vuln: base}
			for label, av := range sw.AffectedVersions {
				rg := Range{}
				if av.FromVersion != "" && av.FromVersion != "*" {
					if av.FromInclusive {
						rg.StartIncl = av.FromVersion
					} else {
						rg.StartExcl = av.FromVersion
					}
				}
				if av.ToVersion != "" && av.ToVersion != "*" {
					if av.ToInclusive {
						rg.EndIncl = av.ToVersion
					} else {
						rg.EndExcl = av.ToVersion
					}
				}
				rec.ranges = append(rec.ranges, rg)
				rec.vuln.Affected = append(rec.vuln.Affected, firstNonEmpty(rg.String(), label))
			}
			if len(sw.PatchedVersions) > 0 {
				rec.vuln.FixedIn = sw.PatchedVersions[0]
			}
			key := strings.ToLower(sw.Type + "|" + sw.Slug)
			idx.bySoftware[key] = append(idx.bySoftware[key], rec)
		}
	}
	return idx, nil
}

func (s *Service) wordfenceLookup(ctx context.Context, cfg Settings, kind, slug, version string) ([]Vuln, bool, error) {
	idx, err := s.loadWordfence(ctx, cfg)
	if err != nil {
		return nil, false, err
	}
	key := wpType(kind) + "|" + strings.ToLower(slug)
	if kind == "wordpress_core" {
		key = "core|wordpress"
	}
	var out []Vuln
	for _, rec := range idx.bySoftware[key] {
		hit := version == ""
		for _, rg := range rec.ranges {
			if version != "" && rg.Contains(version) {
				hit = true
			}
		}
		if hit {
			out = append(out, rec.vuln)
		}
	}
	return out, idx.stale, nil
}

func (s *Service) downloadTo(ctx context.Context, cfg Settings, rawURL string, header map[string]string, path string) error {
	b, err := s.download(ctx, cfg, rawURL, header)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- WPScan ----

type wpsWireVuln struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	FixedIn      string `json:"fixed_in"`
	IntroducedIn string `json:"introduced_in"`
	Published    string `json:"published_date"`
	CreatedAt    string `json:"created_at"`
	VulnType     string `json:"vuln_type"`
	References   struct {
		URL []string `json:"url"`
		CVE []string `json:"cve"`
	} `json:"references"`
	CVSS *struct {
		Score    json.Number `json:"score"`
		Vector   string      `json:"vector"`
		Severity string      `json:"severity"`
	} `json:"cvss"`
}

func (w wpsWireVuln) vuln() Vuln {
	v := Vuln{ID: "WPSCAN-" + w.ID, Title: w.Title, FixedIn: w.FixedIn, Published: dateOnly(firstNonEmpty(w.Published, w.CreatedAt)),
		Sources: []string{SourceWPScan}, References: capList(w.References.URL, 5)}
	for _, c := range w.References.CVE {
		c = strings.ToUpper(strings.TrimSpace(c))
		if !strings.HasPrefix(c, "CVE-") {
			c = "CVE-" + c
		}
		v.Aliases = append(v.Aliases, c)
	}
	if len(v.Aliases) > 0 {
		v.ID, v.Aliases = v.Aliases[0], append(v.Aliases[1:], "WPSCAN-"+w.ID)
	}
	if w.CVSS != nil {
		v.CVSS, _ = w.CVSS.Score.Float64()
		v.Vector, v.Severity = w.CVSS.Vector, strings.ToUpper(w.CVSS.Severity)
	}
	r := Range{StartIncl: w.IntroducedIn, EndExcl: w.FixedIn}
	v.Affected = []string{r.String()}
	return v
}

// wpscanLookup queries one WordPress core version, plugin or theme. Results are
// cached for a day because the free plan allows only 25 requests per day.
func (s *Service) wpscanLookup(ctx context.Context, cfg Settings, kind, slug, version string) ([]Vuln, bool, error) {
	var path string
	switch kind {
	case "wordpress_core":
		if version == "" {
			return nil, false, fmt.Errorf("WPScan needs the WordPress version")
		}
		path = "/wordpresses/" + strings.ReplaceAll(version, ".", "")
	case "wordpress_theme":
		path = "/themes/" + url.PathEscape(strings.ToLower(slug))
	default:
		path = "/plugins/" + url.PathEscape(strings.ToLower(slug))
	}
	all, stale, err := cached(ctx, s, "wpscan", path, answerTTL, func() ([]Vuln, error) {
		var resp map[string]struct {
			Vulnerabilities []wpsWireVuln `json:"vulnerabilities"`
		}
		header := map[string]string{"Authorization": "Token token=" + cfg.key(SourceWPScan)}
		err := s.getJSON(ctx, cfg, http.MethodGet, s.base(SourceWPScan, wpscanURL)+path, header, nil, &resp)
		if err != nil && !isNotFound(err) {
			return nil, err
		}
		all := []Vuln{}
		for _, entry := range resp {
			for _, w := range entry.Vulnerabilities {
				all = append(all, w.vuln())
			}
		}
		return all, nil
	})
	if err != nil {
		return nil, false, err
	}
	if kind == "wordpress_core" || version == "" {
		return all, stale, nil // the core endpoint is already version specific
	}
	var out []Vuln
	for _, v := range all {
		r := Range{EndExcl: v.FixedIn}
		if len(v.Affected) > 0 && strings.HasPrefix(v.Affected[0], ">=") {
			r.StartIncl = strings.TrimPrefix(strings.Fields(v.Affected[0])[0], ">=")
		}
		if r.Contains(version) {
			out = append(out, v)
		}
	}
	return out, stale, nil
}
