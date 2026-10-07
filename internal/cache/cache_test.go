package cache

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAStoredAnswerComesBack(t *testing.T) {
	c := New(time.Minute, 10)

	var loads int
	load := func() (string, error) {
		loads++
		return "the register", nil
	}

	for range 5 {
		got, err := Fetch(c, "k", Messages, load)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if got != "the register" {
			t.Fatalf("got %q", got)
		}
	}
	if loads != 1 {
		t.Errorf("the database was asked %d times, want 1", loads)
	}
}

// TestAWriteDropsWhatItMadeWrong is the mechanism the whole package exists
// for. Expiry is the backstop; this is the thing that keeps publication
// immediate.
func TestAWriteDropsWhatItMadeWrong(t *testing.T) {
	c := New(time.Hour, 10)

	load := func(answer string) func() (string, error) {
		return func() (string, error) { return answer, nil }
	}

	if _, err := Fetch(c, "register", Messages, load("before")); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := Fetch(c, "corpus", Historical, load("untouched")); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// A doléance is published.
	c.Drop(Messages)

	got, _ := Fetch(c, "register", Messages, load("after"))
	if got != "after" {
		t.Errorf("the register still says %q after a publication", got)
	}
	// And nothing else was disturbed: dropping the register must not cost the
	// corpus, which is read on three pages and changes almost never.
	kept, _ := Fetch(c, "corpus", Historical, load("reloaded"))
	if kept != "untouched" {
		t.Errorf("dropping one tag cleared another: %q", kept)
	}
}

func TestAnEntryExpires(t *testing.T) {
	c := New(time.Minute, 10)
	clock := time.Now()
	c.now = func() time.Time { return clock }

	load := func(answer string) func() (string, error) {
		return func() (string, error) { return answer, nil }
	}

	Fetch(c, "k", Messages, load("first")) //nolint:errcheck
	clock = clock.Add(2 * time.Minute)

	got, _ := Fetch(c, "k", Messages, load("second"))
	if got != "second" {
		t.Errorf("an expired entry was served: %q", got)
	}
}

// TestAnErrorIsNeverStored. A database having a bad second would otherwise
// become a whole TTL of an empty register for everybody.
func TestAnErrorIsNeverStored(t *testing.T) {
	c := New(time.Minute, 10)

	failing := func() (string, error) { return "", errors.New("the database is having a moment") }
	if _, err := Fetch(c, "k", Messages, failing); err == nil {
		t.Fatal("the failure was swallowed")
	}

	got, err := Fetch(c, "k", Messages, func() (string, error) { return "recovered", nil })
	if err != nil || got != "recovered" {
		t.Errorf("the failure was cached: %q, %v", got, err)
	}
}

// TestTheKeyCarriesEverythingTheAnswerVariesBy.
//
// A key missing a part is not a slow cache, it is one that answers a different
// question — the register filtered by one subject shown to whoever asked for
// another. And the parts come from a reader, so two different sets of them
// must not be arrangeable into one key.
func TestTheKeyCarriesEverythingTheAnswerVariesBy(t *testing.T) {
	if Keyed("messages", "sante") == Keyed("messages", "logement") {
		t.Error("two different filters share a key")
	}
	// The separator is not a character a value can contain to impersonate
	// another key.
	if Keyed("a", "b:c") == Keyed("a:b", "c") {
		t.Error("the parts can be rearranged into the same key")
	}
	if Keyed("a", "b") == Keyed("ab") {
		t.Error("the parts run together")
	}
}

// TestItStaysBounded. The keys are built from parameters a reader chooses, so
// an unbounded map here is a way to spend all the memory on the machine.
func TestItStaysBounded(t *testing.T) {
	c := New(time.Hour, 8)

	for i := range 100 {
		key := Keyed("messages", string(rune('a'+i%26)), string(rune('a'+i/26)))
		Fetch(c, key, Messages, func() (int, error) { return i, nil }) //nolint:errcheck
	}
	if entries := c.Stats().Entries; entries > 8 {
		t.Errorf("%d entries held, want no more than the limit of 8", entries)
	}
}

// TestItIsSafeUnderConcurrentUse — it is read by every request this backend
// serves, and dropped by the writes happening alongside them.
func TestItIsSafeUnderConcurrentUse(t *testing.T) {
	c := New(time.Minute, 64)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			Fetch(c, Keyed("m", string(rune('a'+i%26))), Messages, //nolint:errcheck
				func() (int, error) { return i, nil })
		}()
		go func() {
			defer wg.Done()
			c.Drop(Messages)
		}()
	}
	wg.Wait()
}
