// Package cache is Rails.cache for a gantry app: values kept in memory by
// key, for a while, so slow work is done once.
//
//	c := cache.New(cache.Options{})
//	stats, err := cache.Fetch(ctx, c, "stats:"+day, time.Hour, func() (Stats, error) {
//		return slowStats(ctx, day)
//	})
//
// Values are stored as JSON, so what's cached is a copy (as Rails' memory
// store dups): changing what Fetch gave changes nothing kept, and a value is
// read back as any type its JSON fits. The store holds up to Size bytes,
// dropping what was used least recently. Separate from web.PageCache, which
// keeps whole responses.
package cache

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Options are a cache's settings.
type Options struct {
	Size int              // bytes kept, keys and values: 32 MB when 0 (Rails' memory store)
	Now  func() time.Time // time.Now when nil; tests set it
}

// Cache is a store of values by key.
type Cache struct {
	o       Options
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // most recently used first
	used    int
	flight  singleflight.Group
}

type entry struct {
	key     string
	value   []byte
	expires time.Time // zero: never
}

// New is an empty cache.
func New(o Options) *Cache {
	if o.Size == 0 {
		o.Size = 32 << 20
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Cache{o: o, entries: map[string]*list.Element{}, order: list.New()}
}

// Fetch is the value cached under key, or, when there's none, fn's, kept
// for ttl (0: until it's dropped for room). Callers asking for a key being
// computed wait for that one computation (singleflight), or until their ctx
// ends. An error isn't cached.
func Fetch[T any](ctx context.Context, c *Cache, key string, ttl time.Duration, fn func() (T, error)) (T, error) {
	if v, ok := Read[T](c, key); ok {
		return v, nil
	}
	var v T
	done := c.flight.DoChan(key, func() (any, error) {
		if raw, ok := c.read(key); ok {
			return raw, nil
		}
		v, err := fn()
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		c.write(key, raw, ttl)
		return raw, nil
	})
	select {
	case res := <-done:
		if res.Err != nil {
			return v, res.Err
		}
		err := json.Unmarshal(res.Val.([]byte), &v)
		return v, err
	case <-ctx.Done():
		return v, ctx.Err()
	}
}

// Read is the value cached under key, if there's one that reads as a T.
func Read[T any](c *Cache, key string) (T, bool) {
	var v T
	raw, ok := c.read(key)
	if !ok || json.Unmarshal(raw, &v) != nil {
		return v, false
	}
	return v, true
}

// Write keeps v under key for ttl (0: until it's dropped for room). A value
// bigger than the whole cache isn't kept.
func (c *Cache) Write(key string, v any, ttl time.Duration) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if !c.write(key, raw, ttl) {
		return ErrTooBig
	}
	return nil
}

// ErrTooBig is a value bigger than the cache.
var ErrTooBig = errors.New("cache: the value is bigger than the cache")

// Delete drops key's value.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
}

// Clear drops everything.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries, c.used = map[string]*list.Element{}, 0
	c.order.Init()
}

func (c *Cache) read(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*entry)
	if !e.expires.IsZero() && !c.o.Now().Before(e.expires) {
		c.remove(el)
		return nil, false
	}
	c.order.MoveToFront(el)
	return e.value, true
}

func (c *Cache) write(key string, raw []byte, ttl time.Duration) bool {
	size := len(key) + len(raw)
	if size > c.o.Size {
		return false
	}
	e := &entry{key: key, value: raw}
	if ttl > 0 {
		e.expires = c.o.Now().Add(ttl)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
	for c.used+size > c.o.Size {
		c.remove(c.order.Back())
	}
	c.entries[key] = c.order.PushFront(e)
	c.used += size
	return true
}

func (c *Cache) remove(el *list.Element) {
	e := c.order.Remove(el).(*entry)
	delete(c.entries, e.key)
	c.used -= len(e.key) + len(e.value)
}
