// Package logbuffer keeps the most recent log lines in memory and fans new
// ones out to subscribers.
//
// It implements io.Writer so zerolog writes to it transparently: no other
// package calls it directly. The only explicit call sites are the binary that
// constructs and wires it, and the HTTP handlers that serve its contents.
package logbuffer

import (
	"bytes"
	"sync"
)

// subscriberQueue is how many lines a subscriber may fall behind before its
// lines start being dropped.
const subscriberQueue = 256

// Buffer is a thread-safe ring of recent log lines with real-time fan-out.
//
// Writes never block on a slow subscriber: a subscriber that cannot keep up
// loses lines rather than stalling the logger, and through it the whole
// application.
type Buffer struct {
	mu      sync.RWMutex
	entries [][]byte
	maxSize int
	subs    map[int]chan []byte
	nextID  int
}

// New returns a Buffer retaining at most maxSize lines.
func New(maxSize int) *Buffer {
	if maxSize < 1 {
		maxSize = 1
	}
	return &Buffer{
		maxSize: maxSize,
		subs:    make(map[int]chan []byte),
	}
}

// Write implements io.Writer. Each call carries one zerolog JSON event.
func (b *Buffer) Write(p []byte) (int, error) {
	line := bytes.TrimRight(p, "\n")
	if len(line) == 0 {
		return len(p), nil
	}

	// zerolog reuses its buffer, so the line has to be copied before it is
	// retained or handed to another goroutine.
	entry := make([]byte, len(line))
	copy(entry, line)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.entries = append(b.entries, entry)
	if len(b.entries) > b.maxSize {
		b.entries = b.entries[len(b.entries)-b.maxSize:]
	}
	for _, ch := range b.subs {
		select {
		case ch <- entry:
		default: // subscriber is behind — drop rather than block the logger
		}
	}
	return len(p), nil
}

// GetAll returns a snapshot of the retained lines, oldest first. It is how a
// new subscriber replays history before it starts receiving live lines.
func (b *Buffer) GetAll() [][]byte {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([][]byte, len(b.entries))
	copy(out, b.entries)
	return out
}

// Subscribe registers a channel receiving every subsequent line. The returned
// id is required to unsubscribe, and the caller must unsubscribe or the
// subscription leaks.
func (b *Buffer) Subscribe() (int, <-chan []byte) {
	ch := make(chan []byte, subscriberQueue)

	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	return id, ch
}

// Unsubscribe removes a subscription and closes its channel. It is safe to
// call with an unknown id.
func (b *Buffer) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}
