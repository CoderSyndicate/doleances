package logbuffer

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestWriteTrimsAndRetainsOrder(t *testing.T) {
	b := New(10)
	for _, line := range []string{"first\n", "second\n", "third\n"} {
		if _, err := b.Write([]byte(line)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	got := b.GetAll()
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriteReportsFullLength(t *testing.T) {
	// io.Writer must report the bytes it was given, newline included, or
	// callers treat the short write as an error.
	b := New(10)
	in := []byte("line\n")
	n, err := b.Write(in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(in) {
		t.Errorf("n = %d, want %d", n, len(in))
	}
}

func TestWriteIgnoresEmptyLines(t *testing.T) {
	b := New(10)
	b.Write([]byte("\n")) //nolint:errcheck
	b.Write([]byte(""))   //nolint:errcheck
	if got := len(b.GetAll()); got != 0 {
		t.Errorf("retained %d entries, want 0", got)
	}
}

func TestRingDropsOldest(t *testing.T) {
	b := New(3)
	for i := range 5 {
		_, _ = fmt.Fprintf(b, "line-%d\n", i)
	}

	got := b.GetAll()
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	if string(got[0]) != "line-2" || string(got[2]) != "line-4" {
		t.Errorf("ring kept %q..%q, want line-2..line-4", got[0], got[2])
	}
}

func TestGetAllSnapshotIsIndependent(t *testing.T) {
	b := New(3)
	b.Write([]byte("one\n")) //nolint:errcheck

	snap := b.GetAll()
	b.Write([]byte("two\n")) //nolint:errcheck

	if len(snap) != 1 {
		t.Errorf("snapshot grew to %d entries; it must not alias the buffer", len(snap))
	}
}

func TestWriteCopiesCallerBuffer(t *testing.T) {
	// zerolog reuses its scratch buffer between events, so a retained entry
	// that aliased it would be silently rewritten by the next log line.
	b := New(3)
	scratch := []byte("first\n")
	b.Write(scratch) //nolint:errcheck
	copy(scratch, []byte("XXXXX"))

	if got := string(b.GetAll()[0]); got != "first" {
		t.Errorf("entry = %q, want %q — the buffer was not copied", got, "first")
	}
}

func TestSubscribeReceivesNewLines(t *testing.T) {
	b := New(10)
	id, ch := b.Subscribe()
	defer b.Unsubscribe(id)

	b.Write([]byte("live\n")) //nolint:errcheck

	select {
	case got := <-ch:
		if string(got) != "live" {
			t.Errorf("received %q, want %q", got, "live")
		}
	case <-time.After(time.Second):
		t.Fatal("no line delivered to subscriber")
	}
}

func TestSubscribeDoesNotReplayHistory(t *testing.T) {
	// History is replayed via GetAll; the channel carries only what follows.
	b := New(10)
	b.Write([]byte("before\n")) //nolint:errcheck

	id, ch := b.Subscribe()
	defer b.Unsubscribe(id)

	select {
	case got := <-ch:
		t.Errorf("subscriber received prior line %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSlowSubscriberDropsRatherThanBlocks(t *testing.T) {
	b := New(10)
	id, ch := b.Subscribe()
	defer b.Unsubscribe(id)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range subscriberQueue * 3 {
			_, _ = fmt.Fprintf(b, "line-%d\n", i)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writes blocked on a subscriber that never reads")
	}

	if got := len(ch); got > subscriberQueue {
		t.Errorf("subscriber queued %d lines, cap is %d", got, subscriberQueue)
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	b := New(10)
	id, ch := b.Subscribe()
	b.Unsubscribe(id)

	if _, open := <-ch; open {
		t.Error("channel still delivering after Unsubscribe")
	}
	b.Unsubscribe(id) // must be safe to repeat
}

func TestUnsubscribedChannelStopsReceiving(t *testing.T) {
	b := New(10)
	id, _ := b.Subscribe()
	b.Unsubscribe(id)

	// A write to a closed subscriber channel would panic.
	b.Write([]byte("after\n")) //nolint:errcheck
}

func TestConcurrentUseIsSafe(t *testing.T) {
	b := New(50)
	var wg sync.WaitGroup

	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				_, _ = fmt.Fprintf(b, "w%d-%d\n", w, i)
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = b.GetAll()
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, ch := b.Subscribe()
			go func() {
				for range ch {
				}
			}()
			time.Sleep(time.Millisecond)
			b.Unsubscribe(id)
		}()
	}
	wg.Wait()
}

func TestNewClampsNonPositiveSize(t *testing.T) {
	b := New(0)
	b.Write([]byte("a\n")) //nolint:errcheck
	b.Write([]byte("b\n")) //nolint:errcheck

	if got := len(b.GetAll()); got != 1 {
		t.Errorf("retained %d entries, want 1", got)
	}
}
