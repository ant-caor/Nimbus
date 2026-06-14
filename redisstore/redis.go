// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

// Package redisstore is nimbus's shared, authoritative L2 tier, backed by
// Redis or Memorystore via rueidis.
//
// It is the single source of versions: each key carries a monotonic version
// minted only here, inside Lua scripts, so the fill invariant holds. Entries
// are hashes; an invalidation leaves a versioned tombstone that gates slower
// in-flight fills. Client-side caching is deliberately disabled: nimbus owns
// the in-process L1, and a second, independently-invalidated cache layer in the
// Redis client would only fight it.
package redisstore

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/rueidis"

	"github.com/ant-caor/nimbus/store"
)

// Version layout. The per-key version must be monotonic across the key's whole
// history, including after the live entry or its tombstone expires out of Redis
// (HGET ver would otherwise read nil and a naive cur+1 would restart at 1,
// letting a slow in-flight fill holding a pre-expiry expected version win a CAS
// it should lose). When the hash is present we keep minting cur+1 (cheap, the
// common path). When it is absent we SEED from the server clock as
// (unixMillis << 10) | seq, starting seq at 1. Because wall-clock time only
// advances across an expiry gap, a re-mint after expiry is always strictly
// greater than any version the key carried before — without a second key, a
// hash tag, or a cross-slot script (TIME takes no keys, so cluster routing is
// unaffected). The 10-bit seq leaves ~1M mints/sec per key of headroom before
// it borrows into the next millisecond, far above any single-key write rate;
// (millis << 10) stays below 2^53 until ~2248, so the value is exact in Lua's
// double and round-trips losslessly. tostring is avoided in favor of
// string.format('%d', ...): Lua 5.1's default number format (%.14g) would
// corrupt a 16-digit version. replicate_commands() switches to effects
// replication so the non-deterministic TIME call is safe (a no-op on Redis 7+,
// required on 6.x).
const versionSeedLua = `
local cur = tonumber(redis.call('HGET', KEYS[1], 'ver'))
local function mint()
  if cur then return cur + 1 end
  local t = redis.call('TIME')
  local ms = (tonumber(t[1]) * 1000) + math.floor(tonumber(t[2]) / 1000)
  return (ms * 1024) + 1
end`

// setCAS mints the next version and writes a live value, guarded by an expected
// version unless force is set. Returns {newOrCurrentVersion, "1" on success}.
var setCAS = rueidis.NewLuaScript(`
redis.replicate_commands()` + versionSeedLua + `
if ARGV[1] ~= '1' and (cur or 0) ~= tonumber(ARGV[2]) then
  return {string.format('%d', cur or 0), '0'}
end
local nv = mint()
redis.call('DEL', KEYS[1])
redis.call('HSET', KEYS[1], 'ver', nv, 'val', ARGV[3], 'sa', ARGV[4], 'fu', ARGV[5], 'su', ARGV[6])
local ttl = tonumber(ARGV[7])
if ttl > 0 then redis.call('PEXPIRE', KEYS[1], ttl) end
return {string.format('%d', nv), '1'}
`)

// compareAndDelete bumps the version and leaves a tombstone, guarded so an older
// invalidation cannot clobber a newer write unless force is set.
var compareAndDelete = rueidis.NewLuaScript(`
redis.replicate_commands()` + versionSeedLua + `
if ARGV[1] ~= '1' and tonumber(ARGV[2]) < (cur or 0) then
  return {string.format('%d', cur or 0), '0'}
end
local nv = mint()
redis.call('DEL', KEYS[1])
redis.call('HSET', KEYS[1], 'ver', nv, 'del', '1')
local ttl = tonumber(ARGV[3])
if ttl > 0 then redis.call('PEXPIRE', KEYS[1], ttl) end
return {string.format('%d', nv), '1'}
`)

type config struct {
	keyPrefix    string
	tagPrefix    string
	tombstoneTTL time.Duration
	tagTTL       time.Duration
}

// Option configures a Store.
type Option func(*config)

// WithKeyPrefix namespaces entry keys in Redis (default "rc:k:").
func WithKeyPrefix(p string) Option { return func(c *config) { c.keyPrefix = p } }

// WithTagPrefix namespaces tag sets in Redis (default "rc:t:").
func WithTagPrefix(p string) Option { return func(c *config) { c.tagPrefix = p } }

// WithTombstoneTTL sets how long a tombstone lives. It must exceed the longest
// plausible loader so a slow in-flight fill cannot resurrect a deleted key
// (default 60s).
func WithTombstoneTTL(d time.Duration) Option { return func(c *config) { c.tombstoneTTL = d } }

// WithTagTTL bounds how long a tag set lives (default 24h).
func WithTagTTL(d time.Duration) Option { return func(c *config) { c.tagTTL = d } }

// Store is the L2 tier. It implements store.VersionedStore.
type Store[V any] struct {
	client rueidis.Client
	codec  store.Codec[V]
	cfg    config
}

// New builds an L2 store over the given rueidis client. The client should be
// created with DisableCache: true; nimbus owns the in-process cache layer.
func New[V any](client rueidis.Client, codec store.Codec[V], opts ...Option) *Store[V] {
	cfg := config{keyPrefix: "rc:k:", tagPrefix: "rc:t:", tombstoneTTL: 60 * time.Second, tagTTL: 24 * time.Hour}
	for _, o := range opts {
		o(&cfg)
	}
	if codec == nil {
		codec = store.JSON[V]()
	}
	return &Store[V]{client: client, codec: codec, cfg: cfg}
}

func (s *Store[V]) k(key string) string { return s.cfg.keyPrefix + key }
func (s *Store[V]) t(tag string) string { return s.cfg.tagPrefix + tag }

// read fetches the raw fields for a key. Entry.Version is set from the stored
// version even for tombstones and absent keys (version 0). found is true only
// for a live value.
func (s *Store[V]) read(ctx context.Context, key string) (store.Entry[V], bool, error) {
	cmd := s.client.B().Hmget().Key(s.k(key)).Field("ver", "del", "val", "sa", "fu", "su").Build()
	arr, err := s.client.Do(ctx, cmd).ToArray()
	if err != nil {
		return store.Entry[V]{}, false, err
	}
	if len(arr) < 6 {
		return store.Entry[V]{}, false, fmt.Errorf("redisstore: unexpected HMGET reply length %d", len(arr))
	}
	verMsg := arr[0]
	if verMsg.IsNil() {
		return store.Entry[V]{}, false, nil // absent: version 0
	}
	verStr, _ := verMsg.ToString()
	ver, _ := strconv.ParseUint(verStr, 10, 64)
	e := store.Entry[V]{Version: ver}
	if del, _ := arr[1].ToString(); del == "1" {
		return e, false, nil // tombstone: version carried, not servable
	}
	valStr, err := arr[2].ToString()
	if err != nil {
		return e, false, nil
	}
	v, derr := s.codec.Decode([]byte(valStr))
	if derr != nil {
		return e, false, derr
	}
	e.Value = v
	e.StoredAt = parseUnixNano(arr[3])
	e.FreshUntil = parseUnixNano(arr[4])
	e.StaleUntil = parseUnixNano(arr[5])
	return e, true, nil
}

// Get implements store.Store.
func (s *Store[V]) Get(ctx context.Context, key string) (store.Entry[V], bool, error) {
	return s.read(ctx, key)
}

// Load implements store.VersionedStore.
func (s *Store[V]) Load(ctx context.Context, key string) (store.Entry[V], bool, error) {
	return s.read(ctx, key)
}

// Set implements store.Store: an unconditional, versioned write of the entry.
func (s *Store[V]) Set(ctx context.Context, key string, e store.Entry[V]) error {
	_, err := s.SetCAS(ctx, key, e.Value, store.ForceVersion, e.FreshUntil, e.StaleUntil, nil)
	return err
}

// SetCAS implements store.VersionedStore.
func (s *Store[V]) SetCAS(ctx context.Context, key string, val V, expect uint64, freshUntil, staleUntil time.Time, tags []string) (store.Entry[V], error) {
	b, err := s.codec.Encode(val)
	if err != nil {
		return store.Entry[V]{}, err
	}
	now := time.Now()
	ttl := s.redisTTL(staleUntil, now)
	force := boolArg(expect == store.ForceVersion)
	res, err := setCAS.Exec(ctx, s.client, []string{s.k(key)}, []string{
		force,
		strconv.FormatUint(expect, 10),
		string(b),
		strconv.FormatInt(now.UnixNano(), 10),
		strconv.FormatInt(freshUntil.UnixNano(), 10),
		strconv.FormatInt(staleUntil.UnixNano(), 10),
		strconv.FormatInt(ttl, 10),
	}).ToArray()
	if err != nil {
		return store.Entry[V]{}, err
	}
	newVer, ok, perr := parseVerFlag(res)
	if perr != nil {
		return store.Entry[V]{}, perr
	}
	if !ok {
		return store.Entry[V]{}, store.ErrVersionConflict
	}
	if len(tags) > 0 {
		if err := s.addTags(ctx, key, tags); err != nil {
			return store.Entry[V]{}, err
		}
	}
	return store.Entry[V]{Value: val, Version: newVer, StoredAt: now, FreshUntil: freshUntil, StaleUntil: staleUntil}, nil
}

// CompareAndDelete implements store.VersionedStore.
func (s *Store[V]) CompareAndDelete(ctx context.Context, key string, version uint64) (uint64, bool, error) {
	force := boolArg(version == store.ForceVersion)
	ttl := s.cfg.tombstoneTTL.Milliseconds()
	res, err := compareAndDelete.Exec(ctx, s.client, []string{s.k(key)}, []string{
		force,
		strconv.FormatUint(version, 10),
		strconv.FormatInt(ttl, 10),
	}).ToArray()
	if err != nil {
		return 0, false, err
	}
	return parseVerFlag(res)
}

// Delete implements store.Store: an unconditional tombstone.
func (s *Store[V]) Delete(ctx context.Context, key string) error {
	_, _, err := s.CompareAndDelete(ctx, key, store.ForceVersion)
	return err
}

// tagScanCount bounds each SSCAN reply, and tagPipelineBatch bounds each
// pipelined tombstone round trip. They are kept equal so one scan page maps to
// one pipeline. 256 amortizes the round trip to near-nothing on Memorystore
// while keeping each reply and pipeline small enough to avoid head-of-line
// stalls for co-tenant operations on the connection.
const (
	tagScanCount     = 256
	tagPipelineBatch = 256
)

// DeleteByTag implements store.VersionedStore. It resolves the tag's members by
// cursor (a bounded SSCAN reply, never a whole-set SMEMBERS), tombstones each
// member by pipelining the per-key compareAndDelete script (one round trip per
// batch, not per key), SREMs the members it tombstoned, and returns the affected
// keys so the caller can broadcast them.
//
// On any error it returns the keys tombstoned so far; the tag set still holds
// the un-SREM'd members, so a retry resumes. A member SADDed concurrently (after
// the cursor paged past it) is never in a processed batch, so it survives for the
// next InvalidateTag — we never blind-delete the whole set.
//
// Note: a key individually invalidated via CompareAndDelete is not removed from
// its tag sets (no per-key reverse index), so it lingers as a dead member until
// the tag TTL. Re-tombstoning such a member here is idempotent and harmless
// (wasted work, not a correctness issue); O(1) tag invalidation via a tag-epoch
// is the post-1.0 path. See DESIGN.md.
func (s *Store[V]) DeleteByTag(ctx context.Context, tag string) ([]string, error) {
	setKey := s.t(tag)
	ttl := strconv.FormatInt(s.cfg.tombstoneTTL.Milliseconds(), 10)
	processed := make([]string, 0, tagScanCount)

	var cursor uint64
	for {
		se, err := s.client.Do(ctx, s.client.B().Sscan().Key(setKey).Cursor(cursor).Count(tagScanCount).Build()).AsScanEntry()
		if err != nil {
			return processed, err
		}
		cursor = se.Cursor

		for start := 0; start < len(se.Elements); start += tagPipelineBatch {
			end := min(start+tagPipelineBatch, len(se.Elements))
			batch := se.Elements[start:end]

			execs := make([]rueidis.LuaExec, len(batch))
			for i, key := range batch {
				// force=1 (last-writer-wins tombstone, mirroring
				// CompareAndDelete with ForceVersion); the expect arg is unused.
				execs[i] = rueidis.LuaExec{Keys: []string{s.k(key)}, Args: []string{"1", "0", ttl}}
			}

			done := make([]string, 0, len(batch))
			var batchErr error
			for i, r := range compareAndDelete.ExecMulti(ctx, s.client, execs...) {
				arr, rerr := r.ToArray()
				if rerr != nil {
					if batchErr == nil {
						batchErr = rerr
					}
					continue // leave this member in the set for a retry
				}
				if _, _, perr := parseVerFlag(arr); perr != nil {
					if batchErr == nil {
						batchErr = perr
					}
					continue
				}
				done = append(done, batch[i])
			}

			// SREM (and report) only the members we actually tombstoned.
			if len(done) > 0 {
				if err := s.client.Do(ctx, s.client.B().Srem().Key(setKey).Member(done...).Build()).Error(); err != nil {
					processed = append(processed, done...)
					return processed, err
				}
				processed = append(processed, done...)
			}
			if batchErr != nil {
				return processed, batchErr
			}
		}

		if cursor == 0 {
			break
		}
	}
	return processed, nil
}

// Close is a no-op. The caller owns the rueidis client passed to New and is
// responsible for closing it, consistent with the rest of nimbus, which never
// closes resources it did not create.
func (s *Store[V]) Close() error { return nil }

// TombstoneTTL reports the configured tombstone lifetime, implementing the
// optional store.TombstoneTTLer interface so nimbus.Build can validate it
// against the refresh timeout.
func (s *Store[V]) TombstoneTTL() time.Duration { return s.cfg.tombstoneTTL }

// addTags associates key with each tag, pipelining the SADD (and the tag-TTL
// refresh) across all tags in a single round trip rather than two per tag. The
// PEXPIRE per add is what keeps an actively-used tag set alive; pipelined
// alongside the SADD it costs no extra round trip.
func (s *Store[V]) addTags(ctx context.Context, key string, tags []string) error {
	cmds := make(rueidis.Commands, 0, len(tags)*2)
	for _, tag := range tags {
		setKey := s.t(tag)
		cmds = append(cmds, s.client.B().Sadd().Key(setKey).Member(key).Build())
		if s.cfg.tagTTL > 0 {
			cmds = append(cmds, s.client.B().Pexpire().Key(setKey).Milliseconds(s.cfg.tagTTL.Milliseconds()).Build())
		}
	}
	for _, r := range s.client.DoMulti(ctx, cmds...) {
		if err := r.Error(); err != nil {
			return err
		}
	}
	return nil
}

// redisTTL is how long Redis should retain a live entry: until it is fully
// expired (past StaleUntil), with a small floor so we never set a non-positive
// TTL on a still-servable entry.
func (s *Store[V]) redisTTL(staleUntil, now time.Time) int64 {
	ms := staleUntil.Sub(now).Milliseconds()
	if ms < 1000 {
		ms = 1000
	}
	return ms
}

func boolArg(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func parseUnixNano(m rueidis.RedisMessage) time.Time {
	if m.IsNil() {
		return time.Time{}
	}
	str, err := m.ToString()
	if err != nil {
		return time.Time{}
	}
	n, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, n)
}

func parseVerFlag(res []rueidis.RedisMessage) (uint64, bool, error) {
	if len(res) < 2 {
		return 0, false, fmt.Errorf("redisstore: unexpected script reply length %d", len(res))
	}
	verStr, err := res[0].ToString()
	if err != nil {
		return 0, false, err
	}
	ver, err := strconv.ParseUint(verStr, 10, 64)
	if err != nil {
		return 0, false, err
	}
	flag, _ := res[1].ToString()
	return ver, flag == "1", nil
}

var (
	_ store.VersionedStore[int] = (*Store[int])(nil)
	_ store.TombstoneTTLer      = (*Store[int])(nil)
)
