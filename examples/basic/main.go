// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

// Command basic is the smallest Nimbus example: an L1-only cache with
// stampede protection. No Redis, no Pub/Sub, no Docker required.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ant-caor/nimbus"
)

func main() {
	// A slow "backend" the cache shields. Returning nimbus.ErrNotFound would
	// negatively cache a key; here every key resolves.
	loader := func(_ context.Context, id string) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return "value-for-" + id, nil
	}

	cache, err := nimbus.NewBuilder[string, string](loader).
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
