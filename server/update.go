package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// HTTP surface for one-click updates from the web page. The real download/verify/swap logic lives in the selfupdate package;
// this only handles the auth boundary, mutual exclusion, progress broadcasting, and telling main "time to exit".
//
// The restart is not done by this process: after staging the new version the process exits with selfupdate.ExitRestart
// and the supervisor script (start.sh / start.bat, the ENTRYPOINT under Docker) starts it again.

// restartCh is closed once the upgrade is ready or a rollback completes; main then exits with ExitRestart.
var (
	restartOnce sync.Once
	restartCh   = make(chan struct{})
)

// RestartRequested returns a channel that is closed when "please exit and let the supervisor restart me".
func RestartRequested() <-chan struct{} { return restartCh }

func requestRestart() { restartOnce.Do(func() { close(restartCh) }) }

// bootState is selfupdate.Bootstrap's verdict for this boot (upgrade succeeded / just rolled back /
// staged file discarded), injected by main so /api/update/check can tell the frontend truthfully how the last upgrade went.
var (
	bootStateMu sync.Mutex
	bootState   selfupdate.State
)

// SetBootUpdateState is called once by main at startup.
func SetBootUpdateState(st selfupdate.State) {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	bootState = st
}

func bootUpdateState() selfupdate.State {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	return bootState
}

// releaseCache caches GitHub's latest release lookup.
//
// The "new version available" hint in the top bar checks once per full page load, and the unauthenticated GitHub API allows
// 60 requests per IP per hour: without caching, a few open tabs or reloads exhaust the quota,
// and then the lookup fails exactly when you want to update. An explicit "check for updates" click can force-bypass the cache.
type releaseCache struct {
	mu  sync.Mutex
	rel *selfupdate.Release
	err error
	at  time.Time
	// fetch is the fetch function, only an injection point for tests; nil uses the real GitHub lookup.
	fetch func(context.Context, *http.Client) (*selfupdate.Release, error)
}

const (
	releaseTTL = 30 * time.Minute
	// Failures are cached briefly too, otherwise every page load waits for a timeout while GitHub is unreachable;
	// but the TTL must be short so it recovers quickly once the network is back.
	releaseErrTTL = 2 * time.Minute
	// Timeout for the lookup. NewClient's 30-minute timeout is for downloading the full package; a version check can't wait that long.
	releaseTimeout = 20 * time.Second
)

var relCache = &releaseCache{}

// get returns the latest Release, without touching the network on a cache hit.
//
// The lock is held during the fetch: concurrent requests queue for the result of the same lookup instead of each hitting GitHub
// (multiple tabs checking right after page load is exactly when rate limiting is most likely).
func (c *releaseCache) get(ctx context.Context, client *http.Client, force bool) (*selfupdate.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force {
		ttl := releaseTTL
		if c.err != nil {
			ttl = releaseErrTTL
		}
		if !c.at.IsZero() && time.Since(c.at) < ttl {
			return c.rel, c.err
		}
	}

	fetch := c.fetch
	if fetch == nil {
		fetch = selfupdate.FetchLatest
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	rel, err := fetch(ctx, client)
	// A cancelled request (user closed the tab) doesn't mean GitHub has a problem; don't cache it,
	// otherwise the next visitor gets a baffling "cancelled" error.
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return c.rel, err
	}
	c.rel, c.err, c.at = rel, err, time.Now()
	return rel, err
}

// updateProgress is one progress update pushed to the frontend.
type updateProgress struct {
	Phase   selfupdate.Phase `json:"phase"`
	Percent int              `json:"percent"` // only meaningful during download; -1 otherwise
	Message string           `json:"message"`
	Version string           `json:"version,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// updateHub holds the progress of one upgrade and broadcasts it to SSE subscribers.
//
// running doubles as a mutex: another POST /api/update/apply during an upgrade gets 409,
// so two goroutines never write to the same artex.new.
type updateHub struct {
	mu      sync.Mutex
	running bool
	cur     updateProgress
	subs    map[chan updateProgress]struct{}
}

var updHub = &updateHub{
	cur:  updateProgress{Phase: selfupdate.PhaseIdle, Percent: -1},
	subs: map[chan updateProgress]struct{}{},
}

// begin claims the upgrade slot; returns false if one is already in progress.
func (h *updateHub) begin(version string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	h.cur = updateProgress{Phase: selfupdate.PhaseDownload, Percent: 0, Message: "Preparing…", Version: version}
	h.fanout(h.cur)
	return true
}

// finish ends an upgrade. A nil err means staging succeeded and a restart is pending.
func (h *updateHub) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = false
	if err != nil {
		h.cur = updateProgress{Phase: selfupdate.PhaseFailed, Percent: -1, Message: "Update failed", Error: err.Error(), Version: h.cur.Version}
	} else {
		h.cur = updateProgress{Phase: selfupdate.PhaseStaged, Percent: 100, Message: "New version is ready, restarting…", Version: h.cur.Version}
	}
	h.fanout(h.cur)
}

func (h *updateHub) publish(ph selfupdate.Phase, pct int, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur = updateProgress{Phase: ph, Percent: pct, Message: msg, Version: h.cur.Version}
	h.fanout(h.cur)
}

// fanout must be called with h.mu held. Subscriber channels are buffered and drop when full:
// progress is disposable transient info, and a stuck SSE connection must never block the upgrade itself.
func (h *updateHub) fanout(p updateProgress) {
	for ch := range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

func (h *updateHub) snapshot() (updateProgress, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur, h.running
}

func (h *updateHub) subscribe() (<-chan updateProgress, func()) {
	ch := make(chan updateProgress, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// updateCheck looks up the latest stable release on GitHub and compares it with the current version.
//
// The frontend also talks to api.github.com directly (GitHub's CORS is *), but **this endpoint is authoritative**:
// the backend does the download, so updating only works if the backend can reach GitHub. The browser reaching it while the server can't
// is common (server on an internal network, or a proxy configured only in the browser); then clicking update is bound to fail,
// so better to report the error truthfully at the check step.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion
	mode := "binary"
	if selfupdate.InDocker() {
		mode = "docker"
	}
	boot := bootUpdateState()
	out := map[string]any{
		"current":     current,
		"mode":        mode,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"has_backup":  selfupdate.HasBackup(),
		"repo":        selfupdate.Repo,
		"boot_notice": boot.Detail,
		"rolled_back": boot.RolledBack,
	}

	// The top-bar hint uses the cache (default); clicking "check for updates" sends force=1 to bypass it.
	force := r.URL.Query().Get("force") != ""
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, force)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}

	latest := rel.TagName
	out["latest"] = latest
	out["notes"] = rel.Body
	out["html_url"] = rel.HTMLURL
	if !rel.PublishedAt.IsZero() {
		out["published_at"] = rel.PublishedAt.Format(time.RFC3339)
	}

	asset := selfupdate.AssetName(latest, runtime.GOOS, runtime.GOARCH)
	out["asset"] = asset
	if a, ok := rel.FindAsset(asset); ok {
		out["asset_available"] = true
		out["size"] = a.Size
	} else {
		out["asset_available"] = false
	}

	cmp, comparable := selfupdate.CompareVersions(current, latest)
	out["comparable"] = comparable
	out["has_update"] = comparable && cmp < 0
	if !comparable {
		// Development builds (dev / git describe with a suffix) have no comparable version. Allowing it would only
		// overwrite the locally debugged binary with a release, so updates are simply refused.
		out["reason"] = fmt.Sprintf("current version %q is not a release version; one-click update is disabled", current)
	}
	writeJSON(w, 200, out)
}

// updateApply downloads and stages the new version, then makes the process exit for the supervisor script to restart.
//
// Returns 202 immediately; the actual work runs in a background goroutine: downloading the full package can take minutes,
// and tying it to the request would get it cut off by reverse-proxy timeouts. Progress goes through /api/update/stream.
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion

	// Use the cache: guarantees that what gets installed is the version the user saw and confirmed in the UI.
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, false)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	cmp, comparable := selfupdate.CompareVersions(current, rel.TagName)
	if !comparable {
		writeErr(w, 400, fmt.Sprintf("current version %q is not a release version; one-click update is disabled", current))
		return
	}
	if cmp >= 0 {
		writeErr(w, 400, fmt.Sprintf("already on the latest version %s", current))
		return
	}
	if !updHub.begin(rel.TagName) {
		writeErr(w, 409, "an update is already in progress")
		return
	}

	go func() {
		// Deliberately uses s.ctx rather than the request ctx: the request ends as soon as the HTTP response returns,
		// and a download tied to it would be cancelled immediately.
		err := selfupdate.Stage(s.ctx, client, rel, current, func(ph selfupdate.Phase, pct int, msg string) {
			updHub.publish(ph, pct, msg)
		})
		updHub.finish(err)
		if err != nil {
			log.Printf("[update] update failed: %v", err)
			return
		}
		log.Printf("[update] %s → %s staged, exiting to complete the swap", current, rel.TagName)
		// Leave a moment to push the last progress update to the frontend before triggering the exit.
		time.Sleep(1500 * time.Millisecond)
		requestRestart()
	}()

	writeJSON(w, 202, map[string]any{"ok": true, "target": rel.TagName})
}

// updateRollback actively rolls back to the previous version (artex.old, backed up before the swap).
func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	if _, running := updHub.snapshot(); running {
		writeErr(w, 409, "an update is in progress; cannot roll back")
		return
	}
	if err := selfupdate.Rollback(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[update] manually rolled back to the previous version, exiting to complete the switch")
	writeJSON(w, 202, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		requestRestart()
	}()
}

// updateStream pushes update progress via SSE.
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := updHub.subscribe()
	defer unsub()

	send := func(p updateProgress) {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// First send the current state so a refreshed page immediately sees an upgrade in progress.
	cur, _ := updHub.snapshot()
	send(cur)

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-ch:
			if !ok {
				return
			}
			send(p)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
