// Command cloudrun is a deployable runcache service for Google Cloud Run: an
// in-process L1, a Memorystore (Redis) L2, and a Pub/Sub push invalidation bus.
// The Terraform in ./terraform provisions everything. See README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pubsub "cloud.google.com/go/pubsub/v2"
	"github.com/redis/rueidis"

	"github.com/ant-caor/runcache"
	"github.com/ant-caor/runcache/invalidation/gcppubsub"
	"github.com/ant-caor/runcache/redisstore"
	"github.com/ant-caor/runcache/store"
	"github.com/ant-caor/runcache/store/memory"
)

func main() {
	ctx := context.Background()
	projectID := mustEnv("PROJECT_ID")
	redisAddr := mustEnv("REDIS_ADDR")
	topic := envOr("PUBSUB_TOPIC", "runcache-invalidation")
	port := envOr("PORT", "8080")

	rdb, err := rueidis.NewClient(rueidis.ClientOption{InitAddress: []string{redisAddr}, DisableCache: true})
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

	psClient, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		log.Fatalf("pubsub: %v", err)
	}
	defer func() { _ = psClient.Close() }()

	bus, err := gcppubsub.NewPush(ctx, psClient, topic)
	if err != nil {
		log.Fatalf("bus: %v", err)
	}

	cache, err := runcache.NewBuilder[string, string](loadItem).
		L1(memory.New[string]()).
		L2(redisstore.New[string](rdb, store.JSON[string]())).
		Bus(bus).
		TTL(30*time.Second, 5*time.Minute).
		Jitter(0.1).
		RefreshMode(runcache.RefreshRequestBound). // safe under request-only CPU
		Build()
	if err != nil {
		log.Fatalf("cache: %v", err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := cache.GetOrLoad(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, runcache.ErrNotFound):
			http.NotFound(w, r)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			writeJSON(w, map[string]string{"id": r.PathValue("id"), "value": v})
		}
	})

	mux.HandleFunc("PUT /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Value string `json:"value"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := cache.Set(r.Context(), r.PathValue("id"), body.Value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := cache.Invalidate(r.Context(), r.PathValue("id")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Pub/Sub push subscription delivers cross-instance invalidations here. It is
	// protected by OIDC push authentication (configured in Terraform).
	mux.Handle("POST /_ah/push", bus.Handler())

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	// Graceful shutdown on SIGTERM (Cloud Run): closing the cache stops the bus
	// subscriber, which tears down this instance's subscription.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = cache.Close()
	}()

	log.Printf("runcache cloudrun listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// loadItem is a placeholder origin loader. Replace it with your real backend
// (database, upstream API). Return runcache.ErrNotFound for a missing item to
// enable negative caching.
func loadItem(_ context.Context, id string) (string, error) {
	time.Sleep(100 * time.Millisecond) // pretend the origin is slow
	return "item-" + id, nil
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing required env %s", k)
	}
	return v
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
