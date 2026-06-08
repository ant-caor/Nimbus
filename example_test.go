package nimbus_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ant-caor/nimbus"
)

// Example shows the simplest use: an L1-only cache with read-through and
// stampede protection. The loader runs once; subsequent reads hit L1.
func Example() {
	loader := func(_ context.Context, key string) (int, error) {
		return len(key), nil // pretend this is an expensive lookup
	}

	cache, err := nimbus.NewBuilder[string, int](loader).
		TTL(time.Minute, 0).
		Build()
	if err != nil {
		panic(err)
	}
	defer func() { _ = cache.Close() }()

	v, _ := cache.GetOrLoad(context.Background(), "hello")
	fmt.Println(v)
	// Output: 5
}

// ExampleErrNotFound shows negative caching: a loader that returns ErrNotFound
// makes nimbus remember the absence, and GetOrLoad reports it as ErrNotFound.
func ExampleErrNotFound() {
	loader := func(_ context.Context, _ string) (int, error) {
		return 0, nimbus.ErrNotFound // the key genuinely does not exist
	}

	cache, err := nimbus.NewBuilder[string, int](loader).
		NegativeTTL(time.Minute).
		Build()
	if err != nil {
		panic(err)
	}
	defer func() { _ = cache.Close() }()

	_, err = cache.GetOrLoad(context.Background(), "missing")
	fmt.Println(errors.Is(err, nimbus.ErrNotFound))
	// Output: true
}
