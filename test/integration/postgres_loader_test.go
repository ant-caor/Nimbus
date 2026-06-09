package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/ant-caor/nimbus"
	"github.com/ant-caor/nimbus/redisstore"
	"github.com/ant-caor/nimbus/store"
	"github.com/ant-caor/nimbus/store/memory"
)

// TestStampedeOverPostgresAndL2 is the full-stack proof: a real Postgres origin
// behind L1 + Redis L2. A burst of concurrent cold requests must collapse to a
// single database query (stampede protection), and a second instance must serve
// from the shared L2 without re-querying the database.
func TestStampedeOverPostgresAndL2(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgDSN)
	require.NoError(t, err)
	defer pool.Close()

	_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS items (id text PRIMARY KEY, val text)`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO items (id, val) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET val = excluded.val`,
		"k", "from-postgres")
	require.NoError(t, err)

	var dbHits atomic.Int64
	loader := func(ctx context.Context, id string) (string, error) {
		dbHits.Add(1)
		var v string
		err := pool.QueryRow(ctx, `SELECT val FROM items WHERE id = $1`, id).Scan(&v)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nimbus.ErrNotFound
		}
		return v, err
	}

	prefix := "pg:" + t.Name() + ":"
	mk := func() nimbus.Cache[string, string] {
		l2 := redisstore.New[string](newRedisClient(t), store.JSON[string](), redisstore.WithKeyPrefix(prefix))
		c, err := nimbus.NewBuilder[string, string](loader).
			L1(memory.New[string]()).
			L2(l2).
			TTL(time.Minute, 0).
			Build()
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	a := mk()

	const n = 100
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := a.GetOrLoad(ctx, "k")
			switch {
			case err != nil:
				errCh <- err
			case v != "from-postgres":
				errCh <- fmt.Errorf("got %q, want %q", v, "from-postgres")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}
	require.Equal(t, int64(1), dbHits.Load(), "stampede should collapse to a single DB query")

	// A second instance sharing the same L2 serves from it, no extra DB query.
	b := mk()
	v, err := b.GetOrLoad(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "from-postgres", v)
	require.Equal(t, int64(1), dbHits.Load(), "second instance should promote from L2, not re-query Postgres")
}
