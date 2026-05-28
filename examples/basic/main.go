// Command basic is the smallest runcache example: an L1-only cache with
// stampede protection. No Redis, no Pub/Sub, no Docker required.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ant-caor/runcache"
)

func main() {
	// A slow "backend" the cache shields. Returning runcache.ErrNotFound would
	// negatively cache a key; here every key resolves.
	loader := func(_ context.Context, id string) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return "value-for-" + id, nil
	}

	cache, err := runcache.NewBuilder[string, string](loader).
		TTL(time.Minute, 0).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = cache.Close() }()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		v, err := cache.GetOrLoad(ctx, "user:42") // first call loads, rest hit L1
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(v)
	}

	fmt.Printf("stats: %+v\n", cache.Stats())
}
