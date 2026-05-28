// Package singleflight is a thin generic wrapper over
// golang.org/x/sync/singleflight. It collapses concurrent calls that share the
// same key into a single execution whose result is shared with all callers,
// which is how runcache protects the backend from cold-start stampedes.
package singleflight

import xsf "golang.org/x/sync/singleflight"

// Group collapses concurrent Do calls that share a key.
type Group[V any] struct {
	g xsf.Group
}

// Do executes fn once per in-flight key. Concurrent callers with the same key
// wait for the in-flight call and receive its result. shared reports whether
// the result was shared with other callers.
func (s *Group[V]) Do(key string, fn func() (V, error)) (v V, shared bool, err error) {
	res, doErr, sh := s.g.Do(key, func() (any, error) { return fn() })
	if doErr != nil {
		var zero V
		return zero, sh, doErr
	}
	return res.(V), sh, nil
}

// Forget drops key's in-flight tracking so the next Do re-executes fn.
func (s *Group[V]) Forget(key string) { s.g.Forget(key) }
