//go:build ignore

package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// releaseCache is the layer protecting the GitHub quota: the unauthenticated API allows only 60 requests/hour/IP,
// while the top bar's "new version available" hint checks on every full page load. Once caching breaks, a user opening a few
// tabs exhausts the quota, and then the lookup fails exactly when they want to update.

func newTestCache(fetch func(context.Context, *http.Client) (*selfupdate.Release, error)) *releaseCache {
	return &releaseCache{fetch: fetch}
}

func TestReleaseCacheServesFromCache(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	for range 5 {
		rel, err := c.get(t.Context(), nil, false)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if rel.TagName != "v0.3.8" {
			t.Fatalf("TagName = %q", rel.TagName)
		}
	}
	if calls != 1 {
		t.Errorf("5 lookups should hit the source only once, got %d", calls)
	}
}

func TestReleaseCacheForceBypasses(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// Clicking "check for updates" must get a live result, otherwise a just-published release only shows up after the cache expires.
	if _, err := c.get(t.Context(), nil, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("force should bypass the cache, expected 2 source hits, got %d", calls)
	}
}

func TestReleaseCacheExpiresAfterTTL(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// Move the stored time back to just past expiry to simulate the TTL running out.
	c.at = time.Now().Add(-releaseTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("should hit the source again after TTL expiry, expected 2, got %d", calls)
	}
}

func TestReleaseCacheUsesShorterTTLForErrors(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return nil, errors.New("github unreachable")
	})

	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("expected an error")
	}
	// Failures are cached briefly too, otherwise every page load waits for a timeout while GitHub is unreachable.
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("errors should be cached briefly, expected 1 source hit, got %d", calls)
	}

	// But the error TTL must be clearly shorter than the success TTL so it heals quickly once the network recovers.
	if releaseErrTTL >= releaseTTL {
		t.Fatalf("error TTL (%v) must be shorter than success TTL (%v)", releaseErrTTL, releaseTTL)
	}
	c.at = time.Now().Add(-releaseErrTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 2 {
		t.Errorf("should retry after the error TTL expires, expected 2, got %d", calls)
	}
}

func TestReleaseCacheDoesNotPoisonOnCallerCancel(t *testing.T) {
	good := &selfupdate.Release{TagName: "v0.3.8"}
	c := newTestCache(func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return good, nil
	})
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}

	// A visitor closing the tab cancels the request. That doesn't mean GitHub has a problem, and "cancelled" must never
	// be written into the cache; otherwise every visitor in the next 30 minutes would get a baffling error.
	c.fetch = func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return nil, ctx.Err()
	}
	c.at = time.Now().Add(-releaseTTL - time.Second) // expire the cache to force a source hit

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, nil, false); err == nil {
		t.Fatal("the error should be passed through when the caller has cancelled")
	}

	// Key invariant: the cancelled call leaves no trace; the cache doesn't hold the "cancelled" error
	// and still keeps the previous good result.
	if c.err != nil {
		t.Fatalf("cancellation error should not be cached, got %v", c.err)
	}
	if c.rel == nil || c.rel.TagName != "v0.3.8" {
		t.Fatalf("cache should keep the previous good result, got %+v", c.rel)
	}

	// The cancellation brought no new data, so the next visitor should hit the source again, and get a normal result
	// without being affected by the earlier cancellation.
	c.fetch = func(context.Context, *http.Client) (*selfupdate.Release, error) {
		return good, nil
	}
	rel, err := c.get(t.Context(), nil, false)
	if err != nil {
		t.Fatalf("a normal request after the cancellation should not fail: %v", err)
	}
	if rel == nil || rel.TagName != "v0.3.8" {
		t.Fatalf("should get a normal result, got %+v", rel)
	}
}
