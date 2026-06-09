package gcppubsub

import (
	"encoding/json"
	"net/http"

	"github.com/ant-caor/runcache/invalidation"
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
// It does not authenticate the request; put it behind OIDC push authentication
// (see examples/cloudrun). It always returns 204 for a well-formed envelope so
// Pub/Sub does not redeliver, including for undecodable payloads.
func PushHandler(handler func(invalidation.Event)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
