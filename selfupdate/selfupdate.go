// Package selfupdate implements ARTEX's one-click update from the web UI: it fetches the new
// binary from a GitHub Release, verifies and stages it, and swaps it in atomically on the next start.
//
// Division of labour (see start.sh / start.bat):
//
//	startup script = a dumb supervisor loop that only decides, from the exit code, whether to relaunch after the process exits
//	this package   = all the error-prone logic (download / SHA256 check / smoke test / swap / rollback on failure)
//
// The swap lives in Go rather than the scripts because the sha256 check and smoke test would need
// two implementations in sh and bat (sha256sum / shasum / certutil), and this is exactly the step that must not fail: swap in a
// binary that cannot run and the supervisor will faithfully relaunch it forever, leaving the user to fix the machine by hand.
//
// A complete upgrade goes through three process starts:
//
//  1. the old server receives /api/update/apply → download and verify → stage artex.new → exit 75
//  2. the script relaunches the old version → Bootstrap finds artex.new → verify + smoke test → swap → exit 75
//  3. the script relaunches, now the new version → Bootstrap records an attempt → clears the marker once startup succeeds
//
// Any failed step falls back to the old version: if step 2 fails verification the staged file is deleted and the old version keeps running; if step 3
// fails to survive until the marker is cleared 3 times in a row (it crashes on start), artex.old is swapped back automatically.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart is the "please relaunch me" exit code for the supervisor (EX_TEMPFAIL). The startup script
// reruns immediately when it sees it, without counting a crash backoff. 0 means the user stopped normally (the script leaves its loop); anything else is a crash.
const ExitRestart = 75

// maxAttempts is the number of start attempts allowed after a swap. Each start of the new version increments the counter; surviving
// settleDelay clears the marker. Crashing maxAttempts times in a row means the new version cannot start at all, so it rolls back automatically.
const maxAttempts = 3

// Paths holds every file involved in an upgrade, all placed in the **directory of the executable**.
// CWD is deliberately not used: as a service the working directory may be / or any path, and using CWD would put the staged file
// somewhere else and break the swap logic entirely.
type Paths struct {
	Dir     string // directory of the executable
	Current string // currently running binary   artex      / artex.exe
	New     string // staged new version          artex.new  / artex.new.exe
	Sum     string // sha256 of the new version (hex)  artex.new.sha256 / artex.new.exe.sha256
	Old     string // old version backed up before the swap  artex.old  / artex.old.exe
	Marker  string // upgrade state marker        artex.upgrade.json
}

// ResolvePaths derives every upgrade path from the current executable.
//
// On Windows .new/.old must also keep the .exe suffix, otherwise the smoke test and execution after the swap both fail,
// so the suffix is stripped first and appended again, keeping naming symmetric on both platforms.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // ".exe" on Windows, usually empty on Unix
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker records the progress of a swap, used to trigger an automatic rollback when the new version cannot start.
type marker struct {
	From     string `json:"from"`     // version before the upgrade
	To       string `json:"to"`       // target version
	Attempts int    `json:"attempts"` // start attempts since the swap
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged removes the staged files. Successful swaps, failed verification and user cancellation all go through it, so a leftover
// artex.new is not retried on the next start.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions compares two version numbers and returns -1/0/1 (a<b / a==b / a>b).
// ok=false means at least one side is not a comparable version (e.g. "dev" from a local development build or
// "0.3.7-2-gabc1234-dirty" from git describe); callers should then disable one-click update,
// otherwise a development build would be "upgraded" to a release and lose uncommitted changes.
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion parses a version like "v0.3.7" / "0.3.7" into [3]int.
//
// Only clean three-part versions are accepted: for non-tag builds build.sh uses git describe to produce
// suffixed versions like "0.3.7-2-gabc1234", and these must be treated as incomparable rather than as
// 0.3.7; otherwise development builds would be misjudged as "already latest" or overwritten by a release.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker reports whether the process runs in a container. Under Docker the swap writes to the container's writable layer,
// and recreating the container with `docker compose up -d` reverts to the image's bundled version. That is expected
// (the user is pulling a new image at that point anyway), but the UI needs to know so it can explain it clearly.
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
