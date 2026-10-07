package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// runTimestampLayout names a run directory by the second it started, in a form
// that sorts chronologically.
const runTimestampLayout = "2006-01-02-15-04-05"

// RunDir is one run's output tree:
//
//	test/run/<YYYY-MM-DD-HH-MI-SS>/
//	  logs/       one JSON log and one console transcript per service
//	  databases/  the backend's SQLite file
//	  storage/    the object store — snapshots and anything else generated
//
// test/run/latest links to the newest run.
type RunDir struct {
	Root      string
	Logs      string
	Databases string
	Storage   string
}

// NewRunDir creates a fresh timestamped run directory under base.
func NewRunDir(base string) (RunDir, error) {
	return OpenRunDir(base, filepath.Join(base, time.Now().Format(runTimestampLayout)))
}

// OpenRunDir prepares a run directory at root, creating what is missing and
// leaving what is already there.
//
// Resuming a run means running **in** its directory, not copying it into a new
// one: the state somebody named is the state they want to carry on with, and a
// fork would leave them looking at logs in one folder and a database in
// another. The directory is therefore reused as it stands — same database,
// same storage, same logs, appended to rather than replaced.
func OpenRunDir(base, root string) (RunDir, error) {
	dir := RunDir{
		Root:      root,
		Logs:      filepath.Join(root, "logs"),
		Databases: filepath.Join(root, "databases"),
		Storage:   filepath.Join(root, "storage"),
	}
	for _, d := range []string{dir.Logs, dir.Databases, dir.Storage} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return RunDir{}, fmt.Errorf("create %s: %w", d, err)
		}
	}

	// A stable path to the run in use, so tailing a log does not mean pasting
	// a timestamp every time. A resumed run is the current one, so the link
	// follows it rather than staying on whatever ran last.
	latest := filepath.Join(base, "latest")
	_ = os.Remove(latest)
	if err := os.Symlink(root, latest); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot link %s: %v\n", latest, err)
	}
	return dir, nil
}

// LogPath is where a service writes its JSON log.
func (r RunDir) LogPath(service string) string {
	return filepath.Join(r.Logs, service+".json")
}

// ConsolePath is where a service's console output is transcribed.
func (r RunDir) ConsolePath(service string) string {
	return filepath.Join(r.Logs, service+".console")
}

// DatabasePath is a service's SQLite file. Each service that owns data gets
// its own file, so two processes never contend on one SQLite lock.
func (r RunDir) DatabasePath(service string) string {
	return filepath.Join(r.Databases, service+".db")
}

// StorageURL is the object store for this run, as the storage flag wants it.
// Each run gets its own directory, so a snapshot generated while testing never
// lands in a previous run's store or in the working tree.
func (r RunDir) StorageURL() string {
	return "file://" + r.Storage
}

// Prune keeps the newest runs and deletes the rest. Directory names sort
// chronologically, so the oldest are simply the first.
// A run holding a file named KeepMarker is never pruned, whatever its age.
//
// Without it, a run worth returning to is deleted by the ordinary use of the
// launcher: ten runs is an afternoon, and the state somebody wanted as a
// baseline goes with the rest. `touch test/run/<run>/KEEP` makes it permanent.
const KeepMarker = "KEEP"

// protect names a run that must survive pruning whatever its age — the one a
// new run is starting from. Deleting it mid-copy would leave a half-seeded
// database, which is worse than keeping one directory too many.
func Prune(base string, keep int, protect ...string) {
	if keep < 1 {
		return
	}

	kept := map[string]bool{}
	for _, path := range protect {
		if path != "" {
			kept[filepath.Base(path)] = true
		}
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}

	var runs []string
	for _, e := range entries {
		// Skip the "latest" symlink and anything not shaped like a run.
		if !e.IsDir() {
			continue
		}
		if _, err := time.Parse(runTimestampLayout, e.Name()); err != nil {
			continue
		}
		if kept[e.Name()] {
			continue
		}
		// An explicit marker outranks the age limit entirely.
		if _, err := os.Stat(filepath.Join(base, e.Name(), KeepMarker)); err == nil {
			continue
		}
		runs = append(runs, e.Name())
	}
	if len(runs) <= keep {
		return
	}
	for _, name := range runs[:len(runs)-keep] {
		path := filepath.Join(base, name)
		if err := os.RemoveAll(path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot remove old run %s: %v\n", path, err)
		}
	}
}
