// Package cache holds answers the database has already given.
//
// # Why it lives in the backend rather than in the services that read
//
// Because the backend owns every write. That is the invariant the whole
// project rests on, and it is what lets this **invalidate** rather than merely
// expire: when a doléance is published, the entry holding the register's front
// page is dropped in the same breath, and the next reader sees it.
//
// A cache in the frontend could only have guessed with a timer. The register's
// own promise is that publication is immediate and irreversible by the
// operator, and a doléance that appeared up to fifteen seconds late for
// reasons nobody could trace would be a quiet erosion of exactly that.
//
// # Every entry still has a lifetime
//
// Invalidation is precise and people are not. A write path somebody adds next
// year will forget to drop something, and the failure mode of a cache with no
// expiry is a register that is wrong until it is restarted. The TTL is a
// backstop, not the mechanism.
package cache

import (
	"strings"
	"sync"
	"time"
)

// Tag names a family of entries that one write invalidates together.
//
// Coarser than a key on purpose. "The register changed" is a thing a write
// knows; "which of the forty filter combinations somebody has asked for since
// this morning are now wrong" is not, and a cache that needed the second
// answer would be one every new endpoint got wrong.
type Tag string

// The families, and what each is invalidated by.
const (
	// Messages covers the register in every shape it is read: the listing, a
	// filtered query, a single doléance. Dropped when one is published,
	// edited or deleted.
	Messages Tag = "messages"

	// Groups covers the map and a group's own page. Dropped on a curator's
	// decision, on an edit being applied, and when visibility changes.
	Groups Tag = "groups"

	// Actions covers what a group is offering. Dropped when one is announced,
	// changed, called off, confirmed or retired.
	Actions Tag = "actions"

	// Historical is the corpus. It changes when a curator corrects a
	// transcription, which is to say almost never — and it is read on the
	// landing page, beside the submission form, and on Voices of the past.
	Historical Tag = "historical"

	// Subjects is the filter vocabulary, including the names it carries in
	// each language. Dropped when a subject is created, renamed or merged.
	Subjects Tag = "subjects"
)

// entry is one stored answer.
type entry struct {
	value   any
	tag     Tag
	expires time.Time
}

// Cache is a keyed store of answers, grouped by tag.
//
// It holds whole decoded values rather than bytes: the caller is the backend
// talking to its own store, so there is nothing to serialise and no point
// paying for it.
//
// **Nothing account-scoped belongs here, ever.** Every value in it is the same
// for every reader by construction — the register, the map, the corpus, the
// vocabulary. A single entry keyed without the reader in it would mean serving
// one person's data to another, which is the one failure this project cannot
// have. See Keyed for what that costs to get wrong.
//
// **And a value that comes back out is shared, not a copy.** Holding decoded
// values is what makes this cheap, and the price is that a slice or a map
// handed to a handler is the same slice every other reader of that key is
// given: writing into it rewrites the stored answer for everybody, and two
// readers writing at once is a data race. A handler that needs to change what
// it got — translating the filter list into the reader's language is the real
// case — copies first.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]entry

	// ttl is the backstop lifetime. Short enough that a missed invalidation
	// is a glitch rather than a bug report, long enough to be worth having.
	ttl time.Duration

	// limit bounds how many answers are held, because the keys are built from
	// query parameters a reader chooses and an unbounded map keyed on
	// somebody else's input is a way to spend all the memory on the machine.
	limit int

	// now is injected so a test can age entries without sleeping.
	now func() time.Time

	hits, misses int64
}

// Defaults.
//
// Fifteen seconds matches the theme overlay, which is the other thing on every
// page, and is the longest this project is willing to be wrong for without a
// reason. The limit is a few hundred answers: the register's filter
// combinations are drawn from a vocabulary of real subjects, not an open set.
const (
	DefaultTTL   = 15 * time.Second
	DefaultLimit = 512
)

// A nil *Cache is a cache that holds nothing, and every method below tolerates
// one.
//
// That is not defensive habit: it is what makes the cache an optimisation
// rather than a dependency. A backend assembled without one — a test, a tool,
// a future caller that has no use for it — must behave identically and simply
// read the database every time. Fetch already assumes this; the methods have
// to agree with it, or the first write path reached without a cache panics in
// middleware rather than returning an answer.

// New builds a cache.
func New(ttl time.Duration, limit int) *Cache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	return &Cache{
		entries: make(map[string]entry, limit),
		ttl:     ttl,
		limit:   limit,
		now:     time.Now,
	}
}

// Get returns a stored answer and whether there was one.
func (c *Cache) Get(key string) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	found, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok || c.now().After(found.expires) {
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return nil, false
	}

	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
	return found.value, true
}

// Put stores an answer under a tag.
func (c *Cache) Put(key string, tag Tag, value any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// Full. Everything expired goes first, and if that frees nothing the
	// write is simply dropped: refusing to store one answer is a cache miss,
	// where evicting something arbitrary to make room is how a bounded map
	// turns into a worse one under exactly the load it was built for.
	if len(c.entries) >= c.limit {
		c.sweep()
		if len(c.entries) >= c.limit {
			return
		}
	}

	c.entries[key] = entry{value: value, tag: tag, expires: c.now().Add(c.ttl)}
}

// Drop invalidates everything under a tag.
//
// Called by the write that made it wrong, in the same request. This is the
// mechanism; the lifetime on each entry is the backstop behind it.
func (c *Cache) Drop(tags ...Tag) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, tag := range tags {
		for key, held := range c.entries {
			if held.tag == tag {
				delete(c.entries, key)
			}
		}
	}
}

// Clear empties the whole cache.
func (c *Cache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]entry, c.limit)
}

// sweep removes what has expired. The caller holds the lock.
func (c *Cache) sweep() {
	now := c.now()
	for key, held := range c.entries {
		if now.After(held.expires) {
			delete(c.entries, key)
		}
	}
}

// Stats is what the cache has been doing, for the maintenance port.
type Stats struct {
	Entries int   `json:"entries"`
	Hits    int64 `json:"hits"`
	Misses  int64 `json:"misses"`
}

// Stats reports the counters.
func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Stats{Entries: len(c.entries), Hits: c.hits, Misses: c.misses}
}

// Keyed builds a cache key from the parts an answer varies by.
//
// **Every part an answer depends on has to be in here.** A key missing one is
// not a slow cache, it is a cache that serves the answer to a different
// question — the register filtered by one subject shown to somebody who asked
// for another.
//
// The separator is a unit byte rather than a colon, so that a value containing
// the separator cannot be arranged to look like a different key: a reader
// chooses several of these.
func Keyed(parts ...string) string {
	return strings.Join(parts, "\x1f")
}

// Fetch returns a cached answer, or loads one and stores it.
//
// The loader runs outside the lock, so a slow query does not stop every other
// reader. Two requests for a cold key will both run it — a thundering herd of
// two is cheaper than the machinery to prevent it, and the alternative holds a
// lock across a database call.
//
// **An error is never cached.** A database having a bad second would otherwise
// become fifteen seconds of a register that is empty for everybody, which is a
// worse page than a slow one.
func Fetch[T any](c *Cache, key string, tag Tag, load func() (T, error)) (T, error) {
	if found, ok := c.Get(key); ok {
		if value, right := found.(T); right {
			return value, nil
		}
		// Two things wrote different types under one key, which is a bug in
		// the keys rather than in the data. Treated as a miss so the page
		// still renders, and the right value replaces it below.
		c.Drop(tag)
	}

	value, err := load()
	if err != nil {
		return value, err
	}
	c.Put(key, tag, value)
	return value, nil
}
