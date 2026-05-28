package runcache

import "errors"

// ErrNotFound is the negative-cache sentinel. A Loader returns ErrNotFound to
// signal that a key is known-absent, which makes runcache store a negative
// entry (subject to the negative TTL). GetOrLoad returns ErrNotFound when it
// serves such a negative hit. It is distinct from a transient load failure,
// which is never cached.
var ErrNotFound = errors.New("runcache: not found")

// ErrClosed is returned by cache operations invoked after Close.
var ErrClosed = errors.New("runcache: cache closed")
