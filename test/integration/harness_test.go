// Package integration runs runcache against real backing services in Docker via
// testcontainers. It lives in its own module so the test infrastructure does
// not leak into the library's dependency graph.
//
// What this harness can and cannot prove: it faithfully exercises the
// correctness protocol (versioning, the fill invariant, cross-instance sharing
// of L2, and cross-instance invalidation over real Pub/Sub) by running several
// independent cache instances against one shared Redis and one Pub/Sub
// emulator. It does NOT reproduce Cloud Run CPU throttling, cold-start latency,
// network partitions, or push load-balancer distribution; those are validated
// only on a real deployment.
package integration

import (
	"context"
	"os"
	"testing"

	"github.com/redis/rueidis"
	"github.com/testcontainers/testcontainers-go"
	tcpubsub "github.com/testcontainers/testcontainers-go/modules/gcloud/pubsub"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

var (
	redisAddr       string
	pubsubProjectID string
	pgDSN           string
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	redisCtr, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		panic("start redis container: " + err.Error())
	}
	redisAddr, err = redisCtr.Endpoint(ctx, "")
	if err != nil {
		panic("redis endpoint: " + err.Error())
	}

	psCtr, err := tcpubsub.Run(ctx, "gcr.io/google.com/cloudsdktool/cloud-sdk:emulators")
	if err != nil {
		panic("start pubsub emulator: " + err.Error())
	}
	pubsubProjectID = psCtr.ProjectID()
	// The Pub/Sub client library auto-connects to the emulator via this env var.
	_ = os.Setenv("PUBSUB_EMULATOR_HOST", psCtr.URI())

	pgCtr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("runcache"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		panic("start postgres container: " + err.Error())
	}
	pgDSN, err = pgCtr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic("postgres dsn: " + err.Error())
	}

	code := m.Run()

	_ = testcontainers.TerminateContainer(redisCtr)
	_ = testcontainers.TerminateContainer(psCtr)
	_ = testcontainers.TerminateContainer(pgCtr)
	os.Exit(code)
}

// newRedisClient returns a rueidis client with client-side caching disabled;
// runcache owns the in-process cache layer.
func newRedisClient(t *testing.T) rueidis.Client {
	t.Helper()
	c, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress:  []string{redisAddr},
		DisableCache: true,
	})
	if err != nil {
		t.Fatalf("new rueidis client: %v", err)
	}
	t.Cleanup(c.Close) // redisstore.Store.Close is a no-op; the caller owns the client
	return c
}
