// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package gcppubsub

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"google.golang.org/api/idtoken"
)

// pushAuth holds the OIDC verification configuration for a push endpoint. A nil
// *pushAuth means authentication is disabled and the handler runs the legacy
// (unauthenticated) path, preserving the existing PushHandler behavior.
type pushAuth struct {
	audience string
	allowed  map[string]struct{} // service-account emails permitted (the JWT "email" claim)
	verifier tokenVerifier
}

// tokenVerifier validates a raw OIDC bearer token and reports the token's
// service-account email and audience. It is an interface so unit tests can
// inject a fake and avoid needing a real Google-signed JWT. verify must return
// a non-nil error for any token whose signature or issuer is not trustworthy;
// the audience and allowlist checks are performed by the caller so it can map
// them to distinct HTTP status codes.
type tokenVerifier interface {
	verify(ctx context.Context, rawToken string) (email, audience string, err error)
}

// googleIDTokenVerifier is the default verifier; it wraps idtoken.Validate,
// which checks the token signature against Google's public certs and validates
// the issuer. Audience is passed empty so an audience mismatch surfaces as a
// distinguishable 403 (decided by the caller) rather than a 401 from the
// validator.
type googleIDTokenVerifier struct{}

func (googleIDTokenVerifier) verify(ctx context.Context, rawToken string) (string, string, error) {
	payload, err := idtoken.Validate(ctx, rawToken, "")
	if err != nil {
		return "", "", err
	}
	email, _ := payload.Claims["email"].(string)
	return email, payload.Audience, nil
}

// newPushAuth builds a pushAuth from an expected audience and an allowlist of
// service-account emails, using the default Google ID-token verifier.
func newPushAuth(audience string, allowedServiceAccounts []string) *pushAuth {
	allowed := make(map[string]struct{}, len(allowedServiceAccounts))
	for _, e := range allowedServiceAccounts {
		if e != "" {
			allowed[e] = struct{}{}
		}
	}
	return &pushAuth{
		audience: audience,
		allowed:  allowed,
		verifier: googleIDTokenVerifier{},
	}
}

// authError carries the HTTP status the handler should return for a failed
// authentication.
type authError struct {
	status int
	msg    string
}

func (e *authError) Error() string { return e.msg }

// authenticate verifies the request's OIDC bearer token against the configured
// audience and service-account allowlist. It returns nil on success, or an
// *authError whose status is 401 (missing/malformed header or
// signature/validation failure) or 403 (audience mismatch or email not in the
// allowlist).
func (a *pushAuth) authenticate(r *http.Request) error {
	raw, err := bearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return &authError{status: http.StatusUnauthorized, msg: err.Error()}
	}
	email, audience, err := a.verifier.verify(r.Context(), raw)
	if err != nil {
		return &authError{status: http.StatusUnauthorized, msg: "invalid OIDC token"}
	}
	if a.audience != "" && audience != a.audience {
		return &authError{status: http.StatusForbidden, msg: "audience mismatch"}
	}
	if _, ok := a.allowed[email]; !ok {
		return &authError{status: http.StatusForbidden, msg: "service account not allowed"}
	}
	return nil
}

// bearerToken extracts the token from an "Authorization: Bearer <jwt>" header.
func bearerToken(header string) (string, error) {
	if header == "" {
		return "", errors.New("missing Authorization header")
	}
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", errors.New("malformed Authorization header")
	}
	tok := strings.TrimSpace(header[len(prefix):])
	if tok == "" {
		return "", errors.New("empty bearer token")
	}
	return tok, nil
}
