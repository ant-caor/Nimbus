// Command demo is a tiny HTTP service that exercises Nimbus with an L1 plus a
// Redis L2, so you can experiment with cross-instance behavior locally via
// docker compose. Two instances (svc-a, svc-b) share one Redis.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	pubsub "cloud.google.com/go/pubsub/v2"
	"github.com/redis/rueidis"

	"github.com/ant-caor/nimbus"
	"github.com/ant-caor/nimbus/invalidation/gcppubsub"
	"github.com/ant-caor/nimbus/redisstore"
	"github.com/ant-caor/nimbus/store"
	"github.com/ant-caor/nimbus/store/memory"
)

func main() {
	redisAddr := env("REDIS_ADDR", "localhost:6379")
	instance := env("INSTANCE_ID", hostname())
	port := env("PORT", "8080")

	rdb, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress:  []string{redisAddr},
		DisableCache: true, // nimbus owns the in-process cache layer
	})
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer rdb.Close()

	l2 := redisstore.New[string](rdb, store.JSON[string]())
	loader := func(_ context.Context, key string) (string, error) {
		time.Sleep(200 * time.Millisecond) // pretend the origin is slow
		return fmt.Sprintf("origin(%s) loaded by %s at %s", key, instance, time.Now().Format(time.RFC3339Nano)), nil
	}
	builder := nimbus.NewBuilder[string, string](loader).
		L1(memory.New[string]()).
		L2(l2).
		TTL(30*time.Second, 60*time.Second)

	// Cross-instance invalidation bus. Enabled when PUBSUB_EMULATOR_HOST is set
	// (docker compose wires the emulator); without it the demo runs L2-only and a
	// non-receiving instance converges on its next L2 read instead of instantly.
	if emuHost := os.Getenv("PUBSUB_EMULATOR_HOST"); emuHost != "" {
		projectID := env("PUBSUB_PROJECT_ID", "nimbus-demo")
		topicID := env("PUBSUB_TOPIC", "nimbus-demo-invalidations")
		psClient, err := pubsub.NewClient(context.Background(), projectID)
		if err != nil {
			log.Fatalf("connect pubsub: %v", err)
		}
		defer psClient.Close()
		// The emulator does not support the subscription expiration policy, so
		// disable it (TTL 0). Each instance gets its own random subscription, so
		// a broadcast fans out to every instance.
		bus, err := gcppubsub.New(context.Background(), psClient, topicID, gcppubsub.WithSubscriptionTTL(0))
		if err != nil {
			log.Fatalf("create pubsub bus: %v", err)
		}
		builder = builder.Bus(bus)
		log.Printf("nimbus demo %q: Pub/Sub bus enabled (emulator %s, topic %q)", instance, emuHost, topicID)
	}

	cache, err := builder.Build()
	if err != nil {
		log.Fatalf("build cache: %v", err)
	}
	defer func() { _ = cache.Close() }()

	mux := http.NewServeMux()

	// GET /get?key=foo -> read-through; reports latency so you can see L1/L2 hits.
	mux.HandleFunc("GET /get", func(w http.ResponseWriter, r *http.Request) {
		key := orDefault(r.URL.Query().Get("key"), "demo")
		start := time.Now()
		v, err := cache.GetOrLoad(r.Context(), key)
		writeJSON(w, map[string]any{
			"instance": instance, "key": key, "value": v,
			"took_ms": time.Since(start).Milliseconds(), "error": errStr(err),
		})
	})

	// POST /set?key=foo&value=bar
	mux.HandleFunc("POST /set", func(w http.ResponseWriter, r *http.Request) {
		key := orDefault(r.URL.Query().Get("key"), "demo")
		val := r.URL.Query().Get("value")
		err := cache.Set(r.Context(), key, val)
		writeJSON(w, map[string]any{"instance": instance, "key": key, "set": val, "error": errStr(err)})
	})

	// POST /invalidate?key=foo
	mux.HandleFunc("POST /invalidate", func(w http.ResponseWriter, r *http.Request) {
		key := orDefault(r.URL.Query().Get("key"), "demo")
		err := cache.Invalidate(r.Context(), key)
		writeJSON(w, map[string]any{"instance": instance, "key": key, "invalidated": true, "error": errStr(err)})
	})

	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"instance": instance, "stats": cache.Stats()})
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("nimbus demo %q listening on :%s (redis %s)", instance, port, redisAddr)
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
