// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package gcppubsub

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ant-caor/nimbus/invalidation"
)

// pushEnvelope is the JSON body Pub/Sub delivers to a push endpoint. The
// message data arrives base64-encoded; encoding/json decodes a base64 string
// into a []byte field automatically.
type pushEnvelope struct {
	Message struct {
		Data      []byte `json:"data"`
		MessageID string `json:"messageId"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

// PushHandler returns an http.Handler that decodes Pub/Sub push deliveries and
// calls handler with the invalidation event. Mount it on your Cloud Run service
// so a push subscription can deliver invalidations inside a request, which is
// throttle-safe under request-only CPU allocation (unlike a streaming pull).
//
// This handler does NOT verify the request itself: it relies on a network-level
// guard (the Cloud Run run.invoker IAM binding for the push service account).
// For in-process defense-in-depth, construct a PushBus with WithPushAuth, whose
// Handler() verifies the Pub/Sub OIDC token before dispatching; that path is
// recommended for production. PushHandler stays available unauthenticated for
// advanced users who terminate auth elsewhere.
//
// It always returns 204 for a well-formed envelope so Pub/Sub does not
// redeliver, including for undecodable payloads.
func PushHandler(handler func(invalidation.Event)) http.Handler {
	return pushHandler(handler, nil)
}

// pushHandler is the shared implementation. When auth is non-nil it verifies the
// request's OIDC token first, returning 401/403 on failure and never reaching
// the dispatch path. When auth is nil the behavior is identical to the legacy
// unauthenticated PushHandler.
func pushHandler(handler func(invalidation.Event), auth *pushAuth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth != nil {
			if err := auth.authenticate(r); err != nil {
				var ae *authError
				if errors.As(err, &ae) {
					http.Error(w, ae.msg, ae.status)
				} else {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
				}
				return
			}
		}
		var env pushEnvelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			http.Error(w, "invalid push envelope", http.StatusBadRequest)
			return
		}
		var ev invalidation.Event
		if err := json.Unmarshal(env.Message.Data, &ev); err != nil {
			w.WriteHeader(http.StatusNoContent) // drop poison message
			return
		}
		handler(ev)
		w.WriteHeader(http.StatusNoContent)
	})
}
