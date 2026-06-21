// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package gcppubsub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ant-caor/nimbus/invalidation"
)

// fakeVerifier is an injectable tokenVerifier so the OIDC handler tests do not
// need a real Google-signed JWT. It maps a raw token string to a fixed result.
type fakeVerifier struct {
	email    string
	audience string
	err      error
}

func (f fakeVerifier) verify(_ context.Context, _ string) (string, string, error) {
	return f.email, f.audience, f.err
}

// pushBody builds a well-formed Pub/Sub push envelope carrying ev.
func pushBody(t *testing.T, ev invalidation.Event) []byte {
	t.Helper()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"message":      map[string]any{"data": data, "messageId": "1"},
		"subscription": "projects/p/subscriptions/s",
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// newAuthHandler builds an authenticated push handler with an injected verifier.
func newAuthHandler(verifier tokenVerifier, audience string, allowed []string, dispatch func(invalidation.Event)) http.Handler {
	auth := &pushAuth{
		audience: audience,
		allowed:  make(map[string]struct{}, len(allowed)),
		verifier: verifier,
	}
	for _, e := range allowed {
		auth.allowed[e] = struct{}{}
	}
	return pushHandler(dispatch, auth)
}

func TestPushAuthValidToken204(t *testing.T) {
	const aud = "https://svc.run.app/_ah/push"
	const sa = "push@proj.iam.gserviceaccount.com"

	got := make(chan invalidation.Event, 1)
	h := newAuthHandler(
		fakeVerifier{email: sa, audience: aud},
		aud, []string{sa},
		func(ev invalidation.Event) { got <- ev },
	)

	ev := invalidation.Event{ID: "e1", Kind: invalidation.KindKey, Keys: []string{"k"}, Version: 9}
	req := httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader(pushBody(t, ev)))
	req.Header.Set("Authorization", "Bearer good.jwt.token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
	select {
	case dispatched := <-got:
		if dispatched.ID != "e1" || dispatched.Version != 9 {
			t.Fatalf("dispatched event = %+v", dispatched)
		}
	default:
		t.Fatal("handler did not dispatch the event")
	}
}

func TestPushAuthInvalidSignature401(t *testing.T) {
	const aud = "https://svc.run.app/_ah/push"
	h := newAuthHandler(
		fakeVerifier{err: errors.New("bad signature")},
		aud, []string{"push@proj.iam.gserviceaccount.com"},
		func(invalidation.Event) { t.Fatal("handler must not dispatch on auth failure") },
	)

	ev := invalidation.Event{ID: "e1"}
	req := httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader(pushBody(t, ev)))
	req.Header.Set("Authorization", "Bearer forged.jwt.token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPushAuthMissingHeader401(t *testing.T) {
	const aud = "https://svc.run.app/_ah/push"
	const sa = "push@proj.iam.gserviceaccount.com"
	h := newAuthHandler(
		fakeVerifier{email: sa, audience: aud},
		aud, []string{sa},
		func(invalidation.Event) { t.Fatal("handler must not dispatch without a token") },
	)

	ev := invalidation.Event{ID: "e1"}
	req := httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader(pushBody(t, ev)))
	// no Authorization header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPushAuthMalformedHeader401(t *testing.T) {
	const aud = "https://svc.run.app/_ah/push"
	const sa = "push@proj.iam.gserviceaccount.com"
	h := newAuthHandler(
		fakeVerifier{email: sa, audience: aud},
		aud, []string{sa},
		func(invalidation.Event) { t.Fatal("handler must not dispatch on a malformed header") },
	)

	for _, hv := range []string{"good.jwt.token", "Bearer", "Bearer ", "Basic abc"} {
		req := httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader(pushBody(t, invalidation.Event{ID: "e1"})))
		req.Header.Set("Authorization", hv)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("header %q: status = %d, want 401", hv, rec.Code)
		}
	}
}

func TestPushAuthAudienceMismatch403(t *testing.T) {
	const wantAud = "https://svc.run.app/_ah/push"
	const sa = "push@proj.iam.gserviceaccount.com"
	h := newAuthHandler(
		fakeVerifier{email: sa, audience: "https://attacker.example/_ah/push"},
		wantAud, []string{sa},
		func(invalidation.Event) { t.Fatal("handler must not dispatch on audience mismatch") },
	)

	req := httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader(pushBody(t, invalidation.Event{ID: "e1"})))
	req.Header.Set("Authorization", "Bearer valid.but.wrong.aud")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestPushAuthEmailNotAllowed403(t *testing.T) {
	const aud = "https://svc.run.app/_ah/push"
	h := newAuthHandler(
		fakeVerifier{email: "intruder@proj.iam.gserviceaccount.com", audience: aud},
		aud, []string{"push@proj.iam.gserviceaccount.com"},
		func(invalidation.Event) { t.Fatal("handler must not dispatch for a non-allowlisted SA") },
	)

	req := httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader(pushBody(t, invalidation.Event{ID: "e1"})))
	req.Header.Set("Authorization", "Bearer valid.but.wrong.sa")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestPushAuthEmptyAudienceSkipsCheck confirms an empty configured audience
// verifies signature + allowlist but does not reject on the aud claim.
func TestPushAuthEmptyAudienceSkipsCheck(t *testing.T) {
	const sa = "push@proj.iam.gserviceaccount.com"
	dispatched := false
	h := newAuthHandler(
		fakeVerifier{email: sa, audience: "anything"},
		"", []string{sa},
		func(invalidation.Event) { dispatched = true },
	)

	req := httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader(pushBody(t, invalidation.Event{ID: "e1"})))
	req.Header.Set("Authorization", "Bearer ok.token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if !dispatched {
		t.Fatal("handler did not dispatch with an empty configured audience")
	}
}

// TestPushHandlerUnauthenticatedStillWorks confirms that when WithPushAuth is
// NOT used, the legacy unauthenticated path is unchanged: no token required, a
// well-formed envelope still dispatches and returns 204.
func TestPushHandlerUnauthenticatedStillWorks(t *testing.T) {
	got := make(chan invalidation.Event, 1)
	h := PushHandler(func(ev invalidation.Event) { got <- ev })

	req := httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader(pushBody(t, invalidation.Event{ID: "e1", Version: 2})))
	// deliberately no Authorization header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	select {
	case ev := <-got:
		if ev.ID != "e1" || ev.Version != 2 {
			t.Fatalf("dispatched event = %+v", ev)
		}
	default:
		t.Fatal("unauthenticated handler did not dispatch")
	}
}

// TestWithPushAuthBuildsVerifier confirms the option wires a default verifier
// and allowlist onto the PushBus (without contacting GCP).
func TestWithPushAuthBuildsVerifier(t *testing.T) {
	var pb PushBus
	WithPushAuth("aud-value", "a@x.iam.gserviceaccount.com", "")(&pb)
	if pb.auth == nil {
		t.Fatal("WithPushAuth did not set auth")
	}
	if pb.auth.audience != "aud-value" {
		t.Fatalf("audience = %q, want %q", pb.auth.audience, "aud-value")
	}
	if _, ok := pb.auth.allowed["a@x.iam.gserviceaccount.com"]; !ok {
		t.Fatal("allowlist missing the configured service account")
	}
	if _, ok := pb.auth.allowed[""]; ok {
		t.Fatal("empty service-account email should be dropped from the allowlist")
	}
	if pb.auth.verifier == nil {
		t.Fatal("default verifier not set")
	}
}
