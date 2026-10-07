package harness

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeRun builds a run directory holding a database and a stored snapshot.
func fakeRun(t *testing.T, root, name string) string {
	t.Helper()

	run := filepath.Join(root, "test", "run", name)
	for _, dir := range []string{"logs", "databases", filepath.Join("storage", "snapshots")} {
		if err := os.MkdirAll(filepath.Join(run, dir), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	write := func(path, content string) {
		if err := os.WriteFile(filepath.Join(run, path), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join("databases", "backend.db"), "the register")
	write(filepath.Join("storage", "snapshots", "one.zip"), "a snapshot")
	write(filepath.Join("logs", "backend.json"), "yesterday's logs")
	return run
}

func TestResolveRunAcceptsEveryUsefulForm(t *testing.T) {
	root := t.TempDir()
	run := fakeRun(t, root, "2026-09-16-16-07-48")

	// The four things somebody actually has to hand: the path the launcher
	// printed, the same path relative to the repo, the bare timestamp, and
	// the "latest" symlink.
	latest := filepath.Join(root, "test", "run", "latest")
	if err := os.Symlink(run, latest); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	for _, name := range []string{
		run,
		filepath.Join("test", "run", "2026-09-16-16-07-48"),
		"2026-09-16-16-07-48",
		"latest",
		"2026-09-16-16-07-48/", // a trailing slash, as a shell completes it
	} {
		resolved, err := ResolveRun(root, name)
		if err != nil {
			t.Errorf("ResolveRun(%q): %v", name, err)
			continue
		}
		// EvalSymlinks resolves /var to /private/var on macOS, so compare the
		// resolved forms rather than the strings.
		wantResolved, _ := filepath.EvalSymlinks(run)
		if resolved != wantResolved {
			t.Errorf("ResolveRun(%q) = %q, want %q", name, resolved, wantResolved)
		}
	}
}

func TestResolveRunReportsMissing(t *testing.T) {
	root := t.TempDir()

	for _, name := range []string{"", "   ", "2020-01-01-00-00-00", "nowhere"} {
		if _, err := ResolveRun(root, name); !errors.Is(err, ErrNoSuchRun) {
			t.Errorf("ResolveRun(%q) err = %v, want ErrNoSuchRun", name, err)
		}
	}
}

func TestPruneKeepsTheRunBeingSeededFrom(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "test", "run")

	oldest := fakeRun(t, root, "2026-01-01-00-00-00")
	for _, name := range []string{
		"2026-02-01-00-00-00", "2026-03-01-00-00-00", "2026-04-01-00-00-00",
	} {
		fakeRun(t, root, name)
	}

	// Keep only two, which would ordinarily delete the oldest.
	Prune(base, 2, oldest)

	if _, err := os.Stat(oldest); err != nil {
		t.Errorf("the run being started from was pruned: %v", err)
	}
}

// TestPruneKeepsMarkedRuns: ten runs is an afternoon's work, so a baseline
// somebody wants to return to has to survive the sweep. Without a marker the
// ordinary use of the launcher deletes it.
func TestPruneKeepsMarkedRuns(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "test", "run")

	marked := fakeRun(t, root, "2026-01-01-00-00-00")
	if err := os.WriteFile(filepath.Join(marked, KeepMarker), nil, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	unmarked := fakeRun(t, root, "2026-01-02-00-00-00")
	for _, name := range []string{"2026-03-01-00-00-00", "2026-04-01-00-00-00"} {
		fakeRun(t, root, name)
	}

	Prune(base, 2)

	if _, err := os.Stat(marked); err != nil {
		t.Errorf("a run marked %s was pruned: %v", KeepMarker, err)
	}
	if _, err := os.Stat(unmarked); err == nil {
		t.Error("an unmarked old run survived the sweep")
	}
}

// TestResumeRunsInPlace is the rule: if the run exists, no new directory is
// made. Resuming means running in that directory, against its database, its
// storage and its logs — not copying it somewhere else.
func TestResumeRunsInPlace(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "test", "run")
	previous := fakeRun(t, root, "2026-09-16-18-29-29")

	before, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read base: %v", err)
	}

	resolved, err := ResolveRun(root, "2026-09-16-18-29-29")
	if err != nil {
		t.Fatalf("ResolveRun: %v", err)
	}
	dir, err := OpenRunDir(base, resolved)
	if err != nil {
		t.Fatalf("OpenRunDir: %v", err)
	}

	// The run in use is the one named.
	if want, _ := filepath.EvalSymlinks(previous); dir.Root != want {
		t.Errorf("running in %q, want %q", dir.Root, want)
	}

	// No new run directory appeared. "latest" may be created, so count only
	// directories shaped like runs.
	after, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	countRuns := func(entries []os.DirEntry) int {
		var n int
		for _, e := range entries {
			if e.IsDir() && e.Name() != "latest" {
				n++
			}
		}
		return n
	}
	if got, want := countRuns(after), countRuns(before); got != want {
		t.Errorf("%d run directories after resuming, want %d — a new one was created", got, want)
	}

	// The database is the one that was already there, untouched.
	content, err := os.ReadFile(filepath.Join(dir.Databases, "backend.db"))
	if err != nil {
		t.Fatalf("read database: %v", err)
	}
	if string(content) != "the register" {
		t.Errorf("database holds %q, want the run's own", content)
	}

	// And "latest" now points at the resumed run, so tailing its log works.
	target, err := filepath.EvalSymlinks(filepath.Join(base, "latest"))
	if err != nil {
		t.Fatalf("read latest: %v", err)
	}
	if target != dir.Root {
		t.Errorf("latest points at %q, want the resumed run %q", target, dir.Root)
	}
}

// TestResumeKeepsExistingState: reopening must not empty the directories it
// finds, or resuming would be a slower way of starting fresh.
func TestResumeKeepsExistingState(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "test", "run")
	previous := fakeRun(t, root, "2026-09-16-18-29-29")

	if _, err := OpenRunDir(base, previous); err != nil {
		t.Fatalf("OpenRunDir: %v", err)
	}

	for path, want := range map[string]string{
		filepath.Join(previous, "databases", "backend.db"):         "the register",
		filepath.Join(previous, "storage", "snapshots", "one.zip"): "a snapshot",
		filepath.Join(previous, "logs", "backend.json"):            "yesterday's logs",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		if string(content) != want {
			t.Errorf("%s holds %q, want %q", path, content, want)
		}
	}
}
