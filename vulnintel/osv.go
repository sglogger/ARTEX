package vulnintel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

const (
	osvBase = "https://api.osv.dev/v1"
)

// osvEcosystems maps common spellings to OSV's canonical ecosystem names.
var osvEcosystems = map[string]string{
	"npm": "npm", "node": "npm", "javascript": "npm", "js": "npm",
	"pypi": "PyPI", "python": "PyPI", "pip": "PyPI",
	"packagist": "Packagist", "composer": "Packagist", "php": "Packagist",
	"maven": "Maven", "java": "Maven",
	"go": "Go", "golang": "Go",
	"rubygems": "RubyGems", "ruby": "RubyGems", "gem": "RubyGems",
	"crates.io": "crates.io", "cargo": "crates.io", "rust": "crates.io",
	"nuget": "NuGet", ".net": "NuGet", "dotnet": "NuGet",
	"hex": "Hex", "pub": "Pub", "swifturl": "SwiftURL",
	"debian": "Debian", "ubuntu": "Ubuntu", "alpine": "Alpine", "rocky linux": "Rocky Linux",
	"almalinux": "AlmaLinux", "github actions": "GitHub Actions", "linux": "Linux",
}

// NormalizeEcosystem returns OSV's spelling, or the input unchanged when unknown
// (OSV also accepts versioned ecosystems such as "Debian:12").
func NormalizeEcosystem(e string) string {
	e = strings.TrimSpace(e)
	base, suffix, _ := strings.Cut(e, ":")
	if canon, ok := osvEcosystems[strings.ToLower(base)]; ok {
		if suffix != "" {
			return canon + ":" + suffix
		}
		return canon
	}
	return e
}

type osvVuln struct {
	ID        string   `json:"id"`
	Aliases   []string `json:"aliases"`
	Summary   string   `json:"summary"`
	Details   string   `json:"details"`
	Modified  string   `json:"modified"`
	Published string   `json:"published"`
	Severity  []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	Affected []struct {
		Package struct {
			Name      string `json:"name"`
			Ecosystem string `json:"ecosystem"`
		} `json:"package"`
		Ranges []struct {
			Type   string `json:"type"`
			Events []struct {
				Introduced   string `json:"introduced"`
				Fixed        string `json:"fixed"`
				LastAffected string `json:"last_affected"`
			} `json:"events"`
		} `json:"ranges"`
		Versions []string `json:"versions"`
	} `json:"affected"`
	References []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"references"`
	DatabaseSpecific struct {
		Severity string   `json:"severity"`
		CWEIDs   []string `json:"cwe_ids"`
	} `json:"database_specific"`
}

func (o osvVuln) vuln(pkg string) Vuln {
	v := Vuln{
		ID: o.ID, Aliases: o.Aliases, Title: truncate(firstNonEmpty(o.Summary, o.Details), 400),
		Severity: strings.ToUpper(o.DatabaseSpecific.Severity), CWE: o.DatabaseSpecific.CWEIDs,
		Published: dateOnly(o.Published), Sources: []string{SourceOSV},
	}
	if v.Severity == "MODERATE" {
		v.Severity = "MEDIUM"
	}
	for _, sv := range o.Severity {
		if strings.HasPrefix(sv.Type, "CVSS") && v.Vector == "" {
			v.Vector = sv.Score
		}
	}
	for _, a := range o.Affected {
		if pkg != "" && !strings.EqualFold(a.Package.Name, pkg) {
			continue
		}
		for _, r := range a.Ranges {
			var intro string
			for _, e := range r.Events {
				switch {
				case e.Introduced != "":
					intro = e.Introduced
				case e.Fixed != "":
					v.Affected = append(v.Affected, rangeLabel(intro, e.Fixed, false))
					if v.FixedIn == "" && r.Type != "GIT" {
						v.FixedIn = e.Fixed
					}
				case e.LastAffected != "":
					v.Affected = append(v.Affected, rangeLabel(intro, e.LastAffected, true))
				}
			}
		}
	}
	v.Affected = capList(uniq(v.Affected), 6)
	for _, r := range o.References {
		if r.Type == "EVIDENCE" || strings.Contains(strings.ToLower(r.URL), "exploit") {
			v.ExploitRefs = append(v.ExploitRefs, r.URL)
		} else if r.Type == "ADVISORY" || r.Type == "WEB" || r.Type == "FIX" {
			v.References = append(v.References, r.URL)
		}
	}
	v.References = capList(v.References, 5)
	return v
}

func rangeLabel(intro, end string, inclusive bool) string {
	r := Range{}
	if intro != "" && intro != "0" {
		r.StartIncl = intro
	}
	if inclusive {
		r.EndIncl = end
	} else {
		r.EndExcl = end
	}
	return r.String()
}

// osvLookup queries OSV for one package version (all known issues when version is empty).
func (s *Service) osvLookup(ctx context.Context, cfg Settings, name, ecosystem, version string) ([]Vuln, bool, error) {
	ecosystem = NormalizeEcosystem(ecosystem)
	key := ecosystem + "|" + name + "|" + version
	all, stale, err := cached(ctx, s, "osv", key, answerTTL, func() ([]osvVuln, error) {
		all := []osvVuln{}
		token := ""
		for page := 0; page < 10; page++ {
			q := map[string]any{"package": map[string]string{"name": name, "ecosystem": ecosystem}}
			if version != "" {
				q["version"] = version
			}
			if token != "" {
				q["page_token"] = token
			}
			body, _ := json.Marshal(q)
			var resp struct {
				Vulns         []osvVuln `json:"vulns"`
				NextPageToken string    `json:"next_page_token"`
			}
			if err := s.getJSON(ctx, cfg, http.MethodPost, s.base(SourceOSV, osvBase)+"/query", nil, bytes.NewReader(body), &resp); err != nil {
				return nil, err
			}
			all = append(all, resp.Vulns...)
			if token = resp.NextPageToken; token == "" {
				break
			}
		}
		return all, nil
	})
	if err != nil {
		return nil, false, err
	}
	out := make([]Vuln, 0, len(all))
	for _, o := range all {
		out = append(out, o.vuln(name))
	}
	return out, stale, nil
}

// osvByID fetches one OSV record (accepts CVE ids too, OSV resolves aliases).
func (s *Service) osvByID(ctx context.Context, cfg Settings, id string) (*osvVuln, bool, error) {
	return cached(ctx, s, "osv_id", id, answerTTL, func() (*osvVuln, error) {
		var o osvVuln
		err := s.getJSON(ctx, cfg, http.MethodGet, s.base(SourceOSV, osvBase)+"/vulns/"+url.PathEscape(id), nil, nil, &o)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &o, nil
	})
}
