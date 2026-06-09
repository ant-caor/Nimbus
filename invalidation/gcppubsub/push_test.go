// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package gcppubsub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ant-caor/nimbus/invalidation"
)

func TestPushHandlerDecodesEnvelope(t *testing.T) {
	var got invalidation.Event
	h := PushHandler(func(ev invalidation.Event) { got = ev })

	ev := invalidation.Event{ID: "e1", Kind: invalidation.KindKey, Keys: []string{"k"}, Version: 7}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	// Pub/Sub delivers message.data base64-encoded; json marshals []byte as base64.
	envelope := map[string]any{
		"message":      map[string]any{"data": data, "messageId": "1"},
		"subscription": "projects/p/subscriptions/s",
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader(body)))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got.ID != "e1" || got.Version != 7 || len(got.Keys) != 1 || got.Keys[0] != "k" {
		t.Fatalf("decoded event = %+v", got)
	}
}

func TestPushHandlerRejectsGarbage(t *testing.T) {
	h := PushHandler(func(invalidation.Event) { t.Fatal("handler should not be called") })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader([]byte("not json"))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
