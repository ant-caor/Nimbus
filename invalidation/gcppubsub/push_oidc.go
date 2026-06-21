// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package gcppubsub

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// googleIDTokenVerifier is the default verifier. It wraps idtoken.Validate,
// which verifies the token signature against Google's public certs and checks
// expiry — but NOT the issuer or the email claim. trustedEmail enforces those:
// a Google issuer and a verified, non-empty email. Audience is passed empty to
// idtoken.Validate so an audience mismatch surfaces as a distinguishable 403
// (decided by the caller) rather than a 401 from the validator.
type googleIDTokenVerifier struct{}

func (googleIDTokenVerifier) verify(ctx context.Context, rawToken string) (string, string, error) {
	payload, err := idtoken.Validate(ctx, rawToken, "")
	if err != nil {
		return "", "", err
	}
	email, err := trustedEmail(payload)
	if err != nil {
		return "", "", err
	}
	return email, payload.Audience, nil
}

// googleIssuers is the set of accepted "iss" claim values for a Google-minted
// OIDC token. idtoken.Validate verifies the signature and expiry but never
// checks the issuer, so it is enforced here.
var googleIssuers = map[string]struct{}{
	"https://accounts.google.com": {},
	"accounts.google.com":         {},
}

// trustedEmail enforces the claims idtoken.Validate leaves to the caller: the
// token must be issued by Google and carry a verified, non-empty email (the
// service-account identity). It returns that email or an error. Kept as a pure
// function of the parsed payload so it is unit-testable without a live Google
// token.
func trustedEmail(p *idtoken.Payload) (string, error) {
	if _, ok := googleIssuers[p.Issuer]; !ok {
		return "", fmt.Errorf("untrusted token issuer %q", p.Issuer)
	}
	email, _ := p.Claims["email"].(string)
	verified, _ := p.Claims["email_verified"].(bool)
	if email == "" || !verified {
		return "", errors.New("token has no verified email claim")
	}
	return email, nil
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
	if audience == "" {
		log.Print("nimbus/gcppubsub: WithPushAuth audience is empty; audience binding " +
			"is disabled — any audience minted by an allowlisted service account is " +
			"accepted. Set an explicit audience in production.")
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
