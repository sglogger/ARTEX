//go:build ignore

package notify

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// This file covers two related hardening measures:
//   (1) delivery addresses must not use the server as a pivot into the internal network / cloud metadata (SSRF)
//   (2) URL validation error messages must not leak credentials embedded in the URL
//
// About the test environment: many tests in this package use httptest fake receivers on 127.0.0.1, which the guard blocks by default.
// So TestMain turns on AllowLocalTargetsEnv globally, and each SSRF test below
// explicitly clears it to assert the **deny by default** behavior.

func TestMain(m *testing.M) {
	// Let regular tests reach local fake receivers; SSRF tests temporarily clear it themselves.
	_ = os.Setenv(AllowLocalTargetsEnv, "1")
	os.Exit(m.Run())
}

// TestDialGuardRejectsLoopbackByDefault is the core SSRF protection assertion:
// with the default config, delivery to a loopback address must be refused at the **connection layer**.
func TestDialGuardRejectsLoopbackByDefault(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "") // escape hatch off = default behavior
	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("delivery to a loopback address should not be allowed by default")
	}
	if hit {
		t.Fatal("the request reached the local service: the guard did not take effect")
	}
	// The error message must tell users how to opt in (a local SMTP relay is a legitimate config).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("the refusal message should explain how to opt in explicitly: %v", err)
	}
}

// TestDialGuardAllowsLoopbackWhenOptedIn is the reverse case: once explicitly enabled it must work,
// or legitimate deployments like a local postfix / internal relay would be ruled out wholesale.
func TestDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("delivery should work after explicitly opting in: %v", err)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", // cloud metadata endpoint: the main reason this function exists
		"169.254.1.1", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // the IPv4-mapped form must be converted back before checking, or it is a bypass
		"",
	}
	for _, s := range blocked {
		if !isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be rejected", s)
		}
	}
	// RFC1918 private ranges are **deliberately allowed**: self-hosted Mattermost / SMTP relays on internal networks are a common legitimate use.
	// This assertion pins that trade-off: if someone later casually adds a private-range check, this fails,
	// forcing a conscious decision (instead of silently breaking a batch of deployments).
	allowed := []string{"10.0.0.5", "172.16.3.4", "192.168.1.10", "8.8.8.8", "2606:4700::1111"}
	for _, s := range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed (private networks are common legitimate delivery targets)", s)
		}
	}
}

// TestValidateHTTPURLRejectsLiteralPrivateTargets covers the early hint at config time:
// literal IPs should be rejected when saving, not after the first delivery fails.
func TestValidateHTTPURLRejectsLiteralPrivateTargets(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "")
	for _, raw := range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/hook",
	} {
		if err := validateHTTPURL(raw); err == nil {
			t.Errorf("%s should be rejected at config time", raw)
		}
	}
	// Public and private addresses pass as usual (private ones are left to the dial phase, which does not block them).
	for _, raw := range []string{"https://oapi.dingtalk.com/robot/send", "http://10.0.0.9/hook"} {
		if err := validateHTTPURL(raw); err != nil {
			t.Errorf("%s should pass validation: %v", raw, err)
		}
	}
}

// TestValidateHTTPURLErrorNeverLeaksCredentials covers the branch the audit found missing in the previous round.
//
// When url.Parse **fails** it returns a *url.Error whose Error() contains the full original URL. The previous round only
// redacted errors returned by http.Client.Do and missed this one; and the "permanent failure path" cases added then
// (file://, gopher://, ftp://) are all parsed successfully by url.Parse and take the scheme branch,
// so all green proved nothing about this path's safety: a false guarantee.
func TestValidateHTTPURLErrorNeverLeaksCredentials(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/%zz?access_token=" + leakProbeToken,         // invalid percent escape
		"https://a.example.com:port/x?access_token=" + leakProbeToken, // non-numeric port
		"http://[::1?access_token=" + leakProbeToken,                  // unbalanced bracket
	}
	for _, raw := range cases {
		// First confirm this input **really** makes url.Parse fail. Without this step, the case could
		// take another branch without anyone noticing (which is how the previous false guarantee came about).
		if _, err := url.Parse(raw); err == nil {
			t.Errorf("%q should fail to parse, otherwise this case does not cover the target branch", raw)
			continue
		}
		err := validateHTTPURL(raw)
		if err == nil {
			t.Errorf("%q should fail validation", raw)
			continue
		}
		assertNoSecret(t, err.Error(), leakProbeToken)
	}
	// Confirm the channel layer's wrapping does not leak the address either.
	t.Setenv(AllowLocalTargetsEnv, "")
	err := (dingTalkChannel{}).Validate(map[string]any{"webhook": cases[0]})
	if err == nil {
		t.Fatal("an invalid address should fail validation")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestEmailDialGuardRejectsLoopbackByDefault covers the SMTP channel's dial guard.
//
// The email channel once used a bare net.Dialer and was the only gap in the whole SSRF defense: a host of
// 169.254.169.254 or 127.0.0.1 would connect directly, and when the smtp.NewClient handshake failed it wrapped
// the line the peer returned into the error, echoed via last_error by the delivery history API: exactly the semi-blind read primitive
// the other channels had already closed; the timing difference between "connection refused" and "timeout" could also be used to probe ports.
//
// This package's TestMain enables AllowLocalTargetsEnv globally (many tests use fake receivers on 127.0.0.1),
// so this case must clear it itself; otherwise it passes whether or not the guard exists,
// which is exactly why the gap was never caught by any test.
func TestEmailDialGuardRejectsLoopbackByDefault(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "") // escape hatch off = default behavior
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Fatal("email delivery to a loopback address should not be allowed by default")
	}
	// No connection should be established at all: the guard blocks it in the Control hook, so EHLO is never sent.
	if f.sawCommand("EHLO") || f.sawCommand("HELO") {
		t.Fatal("an SMTP session was established: the guard did not take effect")
	}
	// The error message must tell users how to opt in (a local postfix relay is a legitimate config).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("the refusal message should explain how to opt in explicitly: %v", err)
	}
}

// TestEmailDialGuardAllowsLoopbackWhenOptedIn is the paired reverse case: once explicitly enabled
// delivery must work normally. Self-hosted internal SMTP / local relays are very common deployments, and the guard must not rule them out wholesale.
func TestEmailDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("local SMTP delivery should work after explicitly opting in: %v", err)
	}
	if !f.sawCommand("EHLO") {
		t.Fatal("no EHLO seen: the session was never really established")
	}
}
