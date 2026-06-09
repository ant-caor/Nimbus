package gcppubsub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ant-caor/runcache/invalidation"
)

// TestPushBusWiring verifies the push path end to end without GCP: Subscribe
// registers the handler, and an HTTP push to Handler() dispatches the decoded
// event to it. A zero-value PushBus is enough since this exercises only the
// receive side (no publishing).
func TestPushBusWiring(t *testing.T) {
	var pb PushBus

	got := make(chan invalidation.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = pb.Subscribe(ctx, func(ev invalidation.Event) { got <- ev }) }()
	time.Sleep(20 * time.Millisecond) // let Subscribe register the handler

	ev := invalidation.Event{ID: "e1", Kind: invalidation.KindKey, Keys: []string{"k"}, Version: 3}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	envelope := map[string]any{"message": map[string]any{"data": data, "messageId": "1"}}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	pb.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_ah/push", bytes.NewReader(body)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	select {
	case dispatched := <-got:
		if dispatched.ID != "e1" || dispatched.Version != 3 || len(dispatched.Keys) != 1 {
			t.Fatalf("dispatched event = %+v", dispatched)
		}
	case <-time.After(time.Second):
		t.Fatal("push handler did not dispatch to the registered handler")
	}
}
