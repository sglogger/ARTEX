package vulnintel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Lookup answers are cached in memory and mirrored to <cacheDir>/cache/<bucket>/,
// so restarts don't repeat rate-limited NVD queries and an unreachable source can
// still answer from its last copy (reported as stale).
const (
	cpeTTL    = 7 * 24 * time.Hour
	answerTTL = 24 * time.Hour
	// cacheMaxAge is when an entry is too old to serve even as a stale fallback.
	cacheMaxAge = 30 * 24 * time.Hour
)

type cachedBlob struct {
	raw []byte
	at  time.Time
}

func cacheFile(dir, bucket, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, "cache", bucket, hex.EncodeToString(sum[:16])+".json")
}

// cacheGet decodes a cached answer into out. found reports any usable entry,
// fresh whether it is younger than ttl; a stale entry is the fallback for failures.
func (s *Service) cacheGet(bucket, key string, ttl time.Duration, out any) (found, fresh bool) {
	mk := bucket + "\x00" + key
	s.mu.Lock()
	b, ok := s.blobs[mk]
	s.mu.Unlock()
	if !ok && s.cacheDir != "" {
		path := cacheFile(s.cacheDir, bucket, key)
		if st, err := os.Stat(path); err == nil {
			if raw, err := os.ReadFile(path); err == nil {
				b, ok = cachedBlob{raw: raw, at: st.ModTime()}, true
				s.mu.Lock()
				s.blobs[mk] = b
				s.mu.Unlock()
			}
		}
	}
	if !ok || time.Since(b.at) > cacheMaxAge || json.Unmarshal(b.raw, out) != nil {
		return false, false
	}
	return true, time.Since(b.at) < ttl
}

// cachePut stores an answer; disk errors only cost the persistence, never the lookup.
func (s *Service) cachePut(bucket, key string, val any) {
	raw, err := json.Marshal(val)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.blobs[bucket+"\x00"+key] = cachedBlob{raw: raw, at: time.Now()}
	s.mu.Unlock()
	if s.cacheDir == "" {
		return
	}
	path := cacheFile(s.cacheDir, bucket, key)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

type liveKey struct{}

// withLive makes every lookup under ctx ask the source itself: no cached answer
// and no stale fallback. Source tests use it so they prove the source works.
func withLive(ctx context.Context) context.Context { return context.WithValue(ctx, liveKey{}, true) }

func isLive(ctx context.Context) bool { v, _ := ctx.Value(liveKey{}).(bool); return v }

// cached runs fetch unless a fresh entry exists; when fetch fails a stale entry
// is returned instead of the error (stale=true).
func cached[T any](ctx context.Context, s *Service, bucket, key string, ttl time.Duration, fetch func() (T, error)) (val T, stale bool, err error) {
	if isLive(ctx) {
		if val, err = fetch(); err == nil {
			s.cachePut(bucket, key, val)
		}
		return val, false, err
	}
	var hit T
	found, fresh := s.cacheGet(bucket, key, ttl, &hit)
	if found && fresh {
		return hit, false, nil
	}
	val, err = fetch()
	if err != nil {
		if found {
			return hit, true, nil
		}
		return val, false, err
	}
	s.cachePut(bucket, key, val)
	return val, false, nil
}

// CacheStats summarises the on-disk cache (lookup answers and downloaded feeds).
type CacheStats struct {
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
	Oldest  time.Time `json:"oldest,omitempty"`
	Newest  time.Time `json:"newest,omitempty"`
	Feeds   []string  `json:"feeds"`
	Entries int       `json:"lookup_entries"`
}

// CacheStats walks the cache directory.
func (s *Service) CacheStats() CacheStats {
	st := CacheStats{Feeds: []string{}}
	if s.cacheDir == "" {
		return st
	}
	_ = filepath.WalkDir(s.cacheDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, ".tmp") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st.Files++
		st.Bytes += info.Size()
		if st.Oldest.IsZero() || info.ModTime().Before(st.Oldest) {
			st.Oldest = info.ModTime()
		}
		if info.ModTime().After(st.Newest) {
			st.Newest = info.ModTime()
		}
		if filepath.Dir(path) == s.cacheDir {
			st.Feeds = append(st.Feeds, strings.TrimSuffix(d.Name(), ".json"))
		} else {
			st.Entries++
		}
		return nil
	})
	return st
}

// ClearCache drops every cached answer and feed, in memory and on disk.
func (s *Service) ClearCache() error {
	s.mu.Lock()
	s.blobs = map[string]cachedBlob{}
	s.feeds = map[string]*feedCache{}
	s.wordfence = nil
	s.mu.Unlock()
	if s.cacheDir == "" {
		return nil
	}
	if err := os.RemoveAll(s.cacheDir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// prune removes cache files past cacheMaxAge; called once per process start.
func (s *Service) prune() {
	if s.cacheDir == "" {
		return
	}
	_ = filepath.WalkDir(filepath.Join(s.cacheDir, "cache"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && time.Since(info.ModTime()) > cacheMaxAge {
			_ = os.Remove(path)
		}
		return nil
	})
}
