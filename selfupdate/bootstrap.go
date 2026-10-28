package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// smokeEnv makes the child process started by the smoke test skip Bootstrap.
//
// Strictly speaking nothing breaks without it: the child's os.Executable() is artex.new, so every derived
// path carries the .new prefix and cannot touch the real upgrade files. But relying on that coincidence is too fragile;
// an explicit short-circuit is obvious and also saves the child a pointless disk probe.
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action is the instruction Bootstrap gives main.
type Action int

const (
	// Continue: start the server as usual.
	Continue Action = iota
	// Restart: exit immediately with ExitRestart so the supervisor script relaunches.
	Restart
)

// State describes the upgrade state at this start, so /api/update/check can tell the UI truthfully
// "whether the last upgrade succeeded or was rolled back".
type State struct {
	Pending     bool   // swapped but not yet confirmed stable
	RolledBack  bool   // an automatic rollback just ran during this start
	FailedStage bool   // staged file failed verification/smoke test and was discarded
	Detail      string // one-line explanation for the user
}

// Bootstrap runs at the very start of main and must be called before listening on any port or opening the database.
//
// Three situations:
//
//  1. staged artex.new exists → verify + smoke test; if it passes swap and request a restart, otherwise discard and keep running the old version
//  2. only the marker file    → a swap just finished; count one attempt, and roll back after enough consecutive failures
//  3. nothing                 → normal start
func Bootstrap() (Action, State) {
	if os.Getenv(smokeEnv) != "" {
		return Continue, State{}
	}
	p, err := ResolvePaths()
	if err != nil {
		log.Printf("[update] skipping bootstrap: %v", err)
		return Continue, State{}
	}

	if _, err := os.Stat(p.New); err == nil {
		return applyStaged(p)
	}

	m, ok := readMarker(p.Marker)
	if !ok {
		return Continue, State{}
	}
	return confirmOrRollback(p, m)
}

// applyStaged handles the "staged file exists" situation: swap if verification passes, discard if it fails.
//
// This is the only place in the whole upgrade path that overwrites the executable, and the last gate: the smoke test blocks
// corrupted downloads, wrong architectures and missing dynamic libraries. Once a binary that cannot run is let through,
// the supervisor script relaunches it tirelessly while the Go code never gets to run, so an automatic rollback is impossible.
func applyStaged(p Paths) (Action, State) {
	m, _ := readMarker(p.Marker)

	if err := verifyStaged(p); err != nil {
		log.Printf("[update] staged new version failed verification and was discarded, keeping the current version: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "new version failed verification and was discarded: " + err.Error()}
	}

	if err := swap(p); err != nil {
		log.Printf("[update] swap failed, keeping the current version: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "swap failed: " + err.Error()}
	}

	// Swap succeeded. Keep the marker for the next start (running the new version) to confirm stability.
	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] failed to write upgrade marker (automatic rollback lost): %v", err)
	}
	log.Printf("[update] swapped to %s, exiting to restart (exit %d)", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// confirmOrRollback handles "a start after a swap": it counts attempts and swaps the old version back once over the limit.
//
// The counter only increments once Go code is running, so it covers failures that "can exec but crash during initialization"
// (incompatible config, port in use, DB migration blew up); "cannot exec at all" is blocked by the smoke test
// before the swap. Together they are complete.
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err := rollback(p); err != nil {
			// If even the rollback failed, stop restarting or we loop forever. Clear the marker
			// and let the process start as it is; if it cannot, the user can at least see why in the logs.
			log.Printf("[update] new version failed to start %d times in a row, and the rollback failed: %v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "new version failed to start and the rollback failed: " + err.Error()}
		}
		log.Printf("[update] new version failed to start %d times in a row, rolled back to %s, exiting to restart (exit %d)",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("new version failed to start, rolled back to %s", orUnknown(m.From))}
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] failed to update upgrade marker: %v", err)
	}
	log.Printf("[update] new version starting (attempt %d/%d), upgrade will be confirmed once it runs stably",
		m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// Settle confirms the new version runs stably and clears the upgrade marker.
//
// main calls it with a delay after the HTTP listener is up: only surviving that long counts; otherwise the marker stays
// and the next start keeps counting attempts until a rollback triggers.
func Settle() {
	p, err := ResolvePaths()
	if err != nil {
		return
	}
	settle(p)
}

func settle(p Paths) {
	if _, ok := readMarker(p.Marker); !ok {
		return // not a post-upgrade start, nothing to do
	}
	if err := os.Remove(p.Marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[update] failed to clear upgrade marker: %v", err)
		return
	}
	log.Printf("[update] new version is stable, upgrade complete (previous version kept as %s)", p.Old)
}

// SettleDelay is how long the process must run to count as "the new version survived".
const SettleDelay = 30 * time.Second

// verifyStaged verifies the staged file: compare SHA256 first, then actually launch it once.
func verifyStaged(p Paths) error {
	want, err := os.ReadFile(p.Sum)
	if err != nil {
		return fmt.Errorf("read checksum: %w", err)
	}
	got, err := fileSHA256(p.New)
	if err != nil {
		return fmt.Errorf("compute checksum: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 mismatch (download corrupted or tampered with)")
	}
	return smokeTest(p.New)
}

// smokeTest launches the new binary with -h to confirm it really runs on this system.
// This blocks a whole class of problems: truncated downloads, wrong architecture (exec format error), missing dependencies.
func smokeTest(bin string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return fmt.Errorf("set executable permission: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-h")
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("smoke test timed out (new binary not responding)")
	}
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("smoke test failed: %v: %s", err, snippet)
	}
	return nil
}

// swap replaces the current binary with the staged new version.
//
// Both Unix and Windows allow renaming a running executable (Windows forbids deleting and
// overwriting it, not renaming), so no per-platform code is needed and there is no need to stop ourselves first.
func swap(p Paths) error {
	// Windows rename does not overwrite an existing target, so a .old left by the previous upgrade must be removed first.
	if err := os.Remove(p.Old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove old backup %s: %w", p.Old, err)
	}
	if err := os.Rename(p.Current, p.Old); err != nil {
		return fmt.Errorf("back up current version: %w", err)
	}
	if err := os.Rename(p.New, p.Current); err != nil {
		// The swap failed but the current version has already been moved away; it must be put back, or the next start has no executable.
		if rerr := os.Rename(p.Old, p.Current); rerr != nil {
			return fmt.Errorf("install new version failed (%v), and restoring the current version failed: %w", err, rerr)
		}
		return fmt.Errorf("install new version: %w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// rollback swaps back the old version backed up by swap.
func rollback(p Paths) error {
	if _, err := os.Stat(p.Old); err != nil {
		return fmt.Errorf("no backup to roll back to %s: %w", p.Old, err)
	}
	// Move the new version that cannot start to .failed for investigation instead of deleting it.
	failed := p.Current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(p.Current, failed); err != nil {
		return fmt.Errorf("move away failed version: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		return fmt.Errorf("restore old version: %w", err)
	}
	return nil
}

// Rollback implements /api/update/rollback: explicitly revert to the previous version.
// It only swaps; the restart is again left to the supervisor script (the caller then exits with ExitRestart).
func Rollback() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Old); err != nil {
		return errors.New("no previous version to roll back to (" + p.Old + " does not exist)")
	}
	cleanStaged(p)
	if err := smokeTest(p.Old); err != nil {
		return fmt.Errorf("previous version cannot run, refusing to roll back: %w", err)
	}
	// Swap current and backup: after a rollback you can still roll forward again.
	tmp := p.Current + ".swap"
	_ = os.Remove(tmp)
	if err := os.Rename(p.Current, tmp); err != nil {
		return fmt.Errorf("move away current version: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		_ = os.Rename(tmp, p.Current)
		return fmt.Errorf("install previous version: %w", err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		log.Printf("[update] tidying backups after rollback failed (does not affect operation): %v", err)
	}
	_ = os.Remove(p.Marker)
	return nil
}

// HasBackup reports whether a previous version is available for rollback, so the UI can decide whether to show the rollback button.
func HasBackup() bool {
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(p.Old)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown version"
	}
	return s
}
