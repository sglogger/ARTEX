package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	jwtKeyFilename = "jwt.key"
	authPassKey    = "auth.password_hash"
	jwtTTL         = 7 * 24 * time.Hour
	keyChars       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// The lower bound matches the setup page's frontend check: validating only in the frontend is no validation at all, since hitting the API directly
	// bypasses it. The upper bound is bcrypt's hard limit: beyond 72 bytes GenerateFromPassword returns
	// ErrPasswordTooLong; rejecting early beats showing the user an obscure "password hashing failed".
	minPasswordRunes = 8
	maxPasswordBytes = 72

	// setupTokenEnv pins the first-run setup token to a known value (headless /
	// automated deployments). When unset a random token is generated at startup and
	// printed to the server console only.
	setupTokenEnv = "ARTEX_SETUP_TOKEN"
	// adminPasswordEnv initialises the admin password at startup without the /setup
	// screen (headless deployments). Ignored once a password is set.
	adminPasswordEnv = "ARTEX_ADMIN_PASSWORD"
	setupTokenLen    = 32
)

// errDataSourceUnavailable is the uniform reply when a password-related read fails. These handlers must never
// treat "can't read" as "not set": authInit once let requests through on a database error, letting unauthenticated requests overwrite
// the existing admin password.
const errDataSourceUnavailable = "data source temporarily unavailable, please try again later"

const (
	errSetupTokenInvalid = "setup token is invalid; enter the setup token printed on the [auth] line of the server console log"
	errTooManyAttempts   = "too many failed attempts, please try again later"
)

// validatePassword returns an empty string when valid, otherwise a reason that can be shown to the user directly.
func validatePassword(pw string) string {
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return fmt.Sprintf("password must be at least %d characters", minPasswordRunes)
	}
	if len(pw) > maxPasswordBytes {
		return fmt.Sprintf("password must not exceed %d bytes", maxPasswordBytes)
	}
	return ""
}

// loadOrCreateJWTKey reads the 32-byte signing key from keyDir/jwt.key. keyDir is
// the project base dir (next to the executable), NOT the browsable workspace root
// (dataDir) — the signing key must never be listable/downloadable via the file
// manager. Legacy installs kept it at dataDir/jwt.key; if present there and not yet
// at the new location, it is migrated (key preserved, so sessions stay valid) and
// the old file removed so it disappears from the workspace. On first run a random
// key is generated and persisted.
func loadOrCreateJWTKey(keyDir, dataDir string) ([]byte, error) {
	path := filepath.Join(keyDir, jwtKeyFilename)
	// one-time migration out of the old in-workspace location.
	if legacy := filepath.Join(dataDir, jwtKeyFilename); legacy != path {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if data, rerr := os.ReadFile(legacy); rerr == nil {
				if werr := os.WriteFile(path, data, 0o600); werr == nil {
					_ = os.Remove(legacy)
					log.Printf("[auth] JWT key migrated from %s to %s (moved out of the browsable workspace)", legacy, path)
				}
			}
		}
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	key, err := randomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate jwt key: %w", err)
	}
	if err := os.WriteFile(path, []byte(key), 0600); err != nil {
		return nil, fmt.Errorf("write jwt key: %w", err)
	}
	log.Printf("[auth] new JWT key written to %s", path)
	return []byte(key), nil
}

// randomToken returns n characters drawn from keyChars with crypto/rand.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	for i := range buf {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(keyChars))))
		if err != nil {
			return "", err
		}
		buf[i] = keyChars[v.Int64()]
	}
	return string(buf), nil
}

// signJWT issues a 7-day HS256 token for user ARTEX.
func signJWT(key []byte) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "ARTEX",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(jwtTTL)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}).SignedString(key)
}

// verifyJWT returns true when tokenStr is a valid, non-expired HS256 token.
func verifyJWT(tokenStr string, key []byte) bool {
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	})
	return err == nil && t.Valid
}

// signingKey derives the HS256 key that signs and verifies tokens for the given
// stored password hash: HMAC-SHA256(jwt.key, hash). Binding the key to the hash
// means every password change — /api/auth/change-password, reset-password.sh, or
// any direct DB edit — invalidates all tokens issued before it, so a session that
// was obtained with a leaked or attacker-set password dies with that password.
// With no password set the raw file key is used; production never issues a token
// in that state (authInit stores the hash first), and DB-less tests keep working.
func (s *Server) signingKey(hash string) []byte {
	if hash == "" {
		return s.jwtKey
	}
	m := hmac.New(sha256.New, s.jwtKey)
	m.Write([]byte(hash))
	return m.Sum(nil)
}

// readPassHash fetches the stored bcrypt hash ("" when unset / no DB). A read
// error is returned as-is, never folded into "": callers must not treat a failed
// read as "no password yet".
func (s *Server) readPassHash() (string, error) {
	if s.m == nil || s.m.pg == nil {
		return "", nil
	}
	h, _, err := s.m.pg.GetSetting(authPassKey)
	if err != nil {
		return "", err
	}
	return h, nil
}

// passHashCacheTTL bounds how long token verification trusts the in-memory hash
// before re-reading it from the DB, so a password reset done outside this process
// (reset-password.sh) revokes old sessions within this window without a restart,
// while steady-state requests cost at most one settings lookup per window.
var passHashCacheTTL = 2 * time.Second

// cachedPassHash returns the password hash, re-reading it from the DB once the
// cache is older than passHashCacheTTL.
func (s *Server) cachedPassHash() (string, error) {
	s.authMu.Lock()
	fresh := s.passHashLoaded && time.Since(s.passHashAt) < passHashCacheTTL
	h := s.passHash
	s.authMu.Unlock()
	if fresh {
		return h, nil
	}
	return s.reloadPassHash()
}

func (s *Server) setPassHash(h string) {
	s.authMu.Lock()
	s.passHash = h
	s.passHashLoaded = true
	s.passHashAt = time.Now()
	s.authMu.Unlock()
}

// reloadPassHash re-reads the hash from the DB and refreshes the cache. On a read
// error the cache is left untouched, so a DB blip is never cached as "no password".
func (s *Server) reloadPassHash() (string, error) {
	h, err := s.readPassHash()
	if err != nil {
		return "", err
	}
	s.setPassHash(h)
	return h, nil
}

// verifyToken validates tok against the key derived from the current password
// hash. On failure it refreshes the cached hash once and retries, so an
// out-of-process password reset takes effect without a restart. A failed hash
// read rejects the token (fail closed).
func (s *Server) verifyToken(tok string) bool {
	if tok == "" {
		return false
	}
	hash, err := s.cachedPassHash()
	if err != nil {
		return false
	}
	if verifyJWT(tok, s.signingKey(hash)) {
		return true
	}
	if fresh, err := s.reloadPassHash(); err == nil && fresh != hash {
		return verifyJWT(tok, s.signingKey(fresh))
	}
	return false
}

// bootstrapAuth runs once at startup. It caches the stored password hash and, when
// no password is set yet, either initialises it from ARTEX_ADMIN_PASSWORD (headless
// deployments) or issues the first-run setup token that /api/auth/init requires.
func (s *Server) bootstrapAuth() {
	if s.m == nil || s.m.pg == nil {
		return
	}
	hash, err := s.reloadPassHash()
	if err != nil {
		// Unknown whether a password exists: neither initialise from the env var
		// nor issue a setup token now. authStatus re-reads on every request.
		log.Printf("[auth] could not read the password state, skipping first-run initialisation: %v", err)
		return
	}
	if hash != "" {
		return
	}
	if pw := os.Getenv(adminPasswordEnv); pw != "" {
		if msg := validatePassword(pw); msg != "" {
			log.Printf("[auth] ignoring %s (%s); initialise with the setup token instead", adminPasswordEnv, msg)
		} else if h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost); err != nil {
			log.Printf("[auth] hashing %s failed: %v", adminPasswordEnv, err)
		} else if inserted, err := s.m.pg.InsertSettingIfAbsent(authPassKey, string(h)); err != nil {
			// Same first-set-only guarantee as authInit: an upsert here would let a
			// second instance starting with a different value overwrite the password.
			log.Printf("[auth] saving %s failed: %v", adminPasswordEnv, err)
		} else if !inserted {
			// Another instance set the password between our read and this write.
			_, _ = s.reloadPassHash()
			return
		} else {
			s.setPassHash(string(h))
			log.Printf("[auth] admin password initialised from %s (username ARTEX)", adminPasswordEnv)
			return
		}
	}
	s.currentSetupToken()
}

// currentSetupToken returns the setup token that gates /api/auth/init while no
// password is set, generating (and printing to the console) one on first use.
// ARTEX_SETUP_TOKEN, when set, is used verbatim and never logged.
func (s *Server) currentSetupToken() string {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.setupToken != "" {
		return s.setupToken
	}
	if v := strings.TrimSpace(os.Getenv(setupTokenEnv)); v != "" {
		s.setupToken = v
		log.Printf("[auth] admin password not set yet; the setup token is the value of %s (not logged)", setupTokenEnv)
		return s.setupToken
	}
	tok, err := randomToken(setupTokenLen)
	if err != nil {
		log.Printf("[auth] generating the setup token failed: %v", err)
		return ""
	}
	s.setupToken = tok
	log.Printf("[auth] admin password not set yet. Setup token: %s", tok)
	log.Print("[auth] enter it in the \"Setup token\" field on /setup, or send it as setup_token in the POST /api/auth/init body. It is never sent over the network and only appears in this console log")
	return s.setupToken
}

// checkSetupToken compares the client-supplied token with the current one in
// constant time. An empty server-side token (generation failed) never matches.
func (s *Server) checkSetupToken(given string) bool {
	want := s.currentSetupToken()
	given = strings.TrimSpace(given)
	return want != "" && subtle.ConstantTimeCompare([]byte(want), []byte(given)) == 1
}

func (s *Server) clearSetupToken() {
	s.authMu.Lock()
	s.setupToken = ""
	s.authMu.Unlock()
}

// limiter returns the per-client failed-attempt limiter shared by login, init and
// change-password.
func (s *Server) limiter() *authLimiter {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.loginLimiter == nil {
		s.loginLimiter = newAuthLimiter()
	}
	return s.loginLimiter
}

// authLimiter throttles repeated authentication failures per client address: the
// first few failures are free, after that each further failure doubles a cooldown
// during which the client gets 429. A success clears the client's record.
type authLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]*authAttempts
}

type authAttempts struct {
	fails int
	last  time.Time
	until time.Time
}

const (
	authFreeFailures    = 5
	authFailureWindow   = 10 * time.Minute
	authCooldownBase    = 30 * time.Second
	authCooldownMax     = 10 * time.Minute
	authLimiterMaxItems = 10000
)

func newAuthLimiter() *authLimiter {
	return &authLimiter{now: time.Now, clients: map[string]*authAttempts{}}
}

// allow reports whether the client may attempt authentication right now.
func (l *authLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.clients[key]
	if a == nil {
		return true
	}
	now := l.now()
	if now.Sub(a.last) > authFailureWindow {
		delete(l.clients, key)
		return true
	}
	return !now.Before(a.until)
}

// fail records a failed attempt and, past the free allowance, starts a cooldown.
func (l *authLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a := l.clients[key]
	if a == nil || now.Sub(a.last) > authFailureWindow {
		if len(l.clients) >= authLimiterMaxItems {
			l.clients = map[string]*authAttempts{}
		}
		a = &authAttempts{}
		l.clients[key] = a
	}
	a.fails++
	a.last = now
	if extra := a.fails - authFreeFailures; extra >= 0 {
		cool := authCooldownBase << uint(extra)
		if cool > authCooldownMax || cool <= 0 {
			cool = authCooldownMax
		}
		a.until = now.Add(cool)
	}
}

func (l *authLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.clients, key)
	l.mu.Unlock()
}

// clientKey identifies the client for throttling. It is the TCP peer only —
// X-Forwarded-For is client-controlled and would let an attacker dodge the limiter
// (or lock out others) by forging it. The forwarded value still shows up in the
// audit log line so an operator behind a reverse proxy can see the real origin.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func clientLogID(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		return r.RemoteAddr + " (X-Forwarded-For: " + xff + ")"
	}
	return r.RemoteAddr
}

// isLoopbackAddr reports whether a listen address only accepts local connections.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	if host == "" { // ":8787" / "0.0.0.0:8787" style — every interface
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// WarnIfExposed logs a warning when the admin password is still unset and the
// server accepts connections from other hosts: the setup token blocks a remote
// takeover, but the port should still not be reachable before setup is done.
func (s *Server) WarnIfExposed(addr string) {
	if s.m == nil || s.m.pg == nil || isLoopbackAddr(addr) {
		return
	}
	if h, err := s.cachedPassHash(); err != nil || h != "" {
		return
	}
	log.Printf("[auth] warning: the admin password is not set yet and the server listens on %q, reachable from other hosts. Initialisation still requires the setup token, but do not expose this port until setup is finished", addr)
}

// extractToken reads the JWT from Authorization: Bearer header,
// artex_token cookie, or ?token= query param (for SSE connections).
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("artex_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// requireAuth wraps h with JWT validation.
// /api/auth/* and /api/health are exempt.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/auth/") || p == "/api/health" {
			h.ServeHTTP(w, r)
			return
		}
		tok := extractToken(r)
		if tok == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		if !s.verifyToken(tok) {
			writeErr(w, 401, "token is invalid or expired")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// GET /api/auth/status — reports whether the admin password has been initialised.
// A read failure must return 503, not initialized:false: on initialized:false the frontend sends the user
// to /setup to set a password (login/page.tsx), so wrapping a database failure as 200 pushes the user toward
// overwriting the existing password. While no password is set, setup_token_required tells /setup to ask for
// the console setup token (and makes sure one has been issued and printed).
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	hash, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	s.setPassHash(hash)
	if hash == "" {
		s.currentSetupToken()
	}
	writeJSON(w, 200, map[string]any{"initialized": hash != "", "setup_token_required": hash == ""})
}

// POST /api/auth/init — sets the password for the first time; rejected if already
// set. The route is reachable without a session (nothing exists yet to log in
// with), so it is gated by the setup token printed on the server console: a
// remote client who merely reaches the port cannot claim the admin account.
func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, 429, errTooManyAttempts)
		return
	}
	var req struct {
		Password   string `json:"password"`
		SetupToken string `json:"setup_token"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "malformed request")
		return
	}
	// Serialise the check-then-set so two concurrent first-run requests cannot both
	// pass the "not yet set" check.
	s.initMu.Lock()
	defer s.initMu.Unlock()
	existing, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	s.setPassHash(existing)
	if existing != "" {
		writeErr(w, 403, "password already set")
		return
	}
	if !s.checkSetupToken(req.SetupToken) {
		s.limiter().fail(key)
		log.Printf("[auth] initial setup rejected: setup token mismatch (client=%s)", clientLogID(r))
		writeErr(w, 403, errSetupTokenInvalid)
		return
	}
	if req.Password == "" {
		writeErr(w, 400, "password must not be empty")
		return
	}
	if msg := validatePassword(req.Password); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "password hashing failed")
		return
	}
	// Use INSERT ... ON CONFLICT DO NOTHING rather than upsert: the GetSetting above is only
	// a fail-fast path; the real "can only be set once" guarantee rests on the primary key constraint. bcrypt takes tens of milliseconds,
	// during which another request may well set the password first, and the read check itself can fail due to faults.
	inserted, err := pg.InsertSettingIfAbsent(authPassKey, string(hash))
	if err != nil {
		writeErr(w, 500, "save failed: "+err.Error())
		return
	}
	if !inserted {
		writeErr(w, 403, "password already set")
		return
	}
	s.setPassHash(string(hash))
	s.clearSetupToken()
	s.limiter().reset(key)
	tok, err := signJWT(s.signingKey(string(hash)))
	if err != nil {
		writeErr(w, 500, "token generation failed")
		return
	}
	log.Printf("[auth] admin password initialised (client=%s)", clientLogID(r))
	writeJSON(w, 200, map[string]any{"token": tok})
}

// POST /api/auth/change-password — changes the admin password. Requires a valid
// token (this route is under /api/auth/* which requireAuth exempts, so the token
// is validated here) AND the current password. Every previously issued token
// stops working (the signing key is bound to the hash); the response carries a
// fresh token so the caller's own session continues.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if !s.verifyToken(extractToken(r)) {
		writeErr(w, 401, "unauthorized")
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, 429, errTooManyAttempts)
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "malformed request")
		return
	}
	if req.NewPassword == "" {
		writeErr(w, 400, "new password must not be empty")
		return
	}
	if msg := validatePassword(req.NewPassword); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, "password not initialized, please set a password first")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err != nil {
		s.limiter().fail(key)
		log.Printf("[auth] password change rejected: current password mismatch (client=%s)", clientLogID(r))
		writeErr(w, 401, "current password is incorrect")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "password hashing failed")
		return
	}
	if err := pg.SetSetting(authPassKey, string(newHash)); err != nil {
		writeErr(w, 500, "save failed: "+err.Error())
		return
	}
	s.setPassHash(string(newHash))
	s.limiter().reset(key)
	tok, err := signJWT(s.signingKey(string(newHash)))
	if err != nil {
		writeErr(w, 500, "token generation failed")
		return
	}
	log.Printf("[auth] admin password changed; all earlier sessions are invalidated (client=%s)", clientLogID(r))
	writeJSON(w, 200, map[string]any{"ok": true, "token": tok})
}

// POST /api/auth/login — validates username/password and returns a JWT.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, 429, errTooManyAttempts)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "malformed request")
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, "password not initialized, please set a password first")
		return
	}
	// Always run the bcrypt comparison so a wrong username costs the same as a
	// wrong password (no username oracle, no timing shortcut).
	pwErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password))
	if req.Username != "ARTEX" || pwErr != nil {
		s.limiter().fail(key)
		log.Printf("[auth] login failed (client=%s)", clientLogID(r))
		writeErr(w, 401, "invalid username or password")
		return
	}
	s.setPassHash(hash)
	s.limiter().reset(key)
	tok, err := signJWT(s.signingKey(hash))
	if err != nil {
		writeErr(w, 500, "token generation failed")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}
