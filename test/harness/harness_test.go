package harness

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaticPortsAreInTheTestRange(t *testing.T) {
	// Test runs stay in 53xx so they never collide with the services' own
	// defaults, nor with another project's services on this machine.
	p := StaticPorts()
	for name, port := range map[string]int{
		"backend app":          p.BackendApp,
		"console app":          p.ConsoleApp,
		"frontend app":         p.FrontendApp,
		"backend maintenance":  p.BackendMaintenance,
		"console maintenance":  p.ConsoleMaintenance,
		"frontend maintenance": p.FrontendMaintenance,
	} {
		if port < 5300 || port > 5399 {
			t.Errorf("%s = %d, want a port in 53xx", name, port)
		}
	}
}

func TestStaticPortsAreDistinct(t *testing.T) {
	p := StaticPorts()
	seen := map[int]bool{}
	for _, port := range []int{
		p.BackendApp, p.ConsoleApp, p.FrontendApp,
		p.BackendMaintenance, p.ConsoleMaintenance, p.FrontendMaintenance,
	} {
		if seen[port] {
			t.Errorf("port %d is assigned twice", port)
		}
		seen[port] = true
	}
}

func TestAllocatePortsAreDistinct(t *testing.T) {
	p, err := AllocatePorts()
	if err != nil {
		t.Fatalf("AllocatePorts: %v", err)
	}

	seen := map[int]bool{}
	for _, port := range []int{
		p.BackendApp, p.ConsoleApp, p.FrontendApp,
		p.BackendMaintenance, p.ConsoleMaintenance, p.FrontendMaintenance,
	} {
		if port == 0 {
			t.Error("a port was left unassigned")
		}
		if seen[port] {
			t.Errorf("port %d is assigned twice", port)
		}
		seen[port] = true
	}
}

func TestCheckAvailable(t *testing.T) {
	free, err := FreePort()
	if err != nil {
		t.Fatalf("FreePort: %v", err)
	}
	if err := CheckAvailable(map[string]int{"free": free}); err != nil {
		t.Errorf("CheckAvailable: %v", err)
	}
}

func TestCheckAvailableDetectsLoopbackOnlyListener(t *testing.T) {
	// The case that matters: with SO_REUSEADDR a wildcard bind succeeds even
	// though something else holds the loopback address, and the run would then
	// silently talk to that other process.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck

	port := ln.Addr().(*net.TCPAddr).Port
	err = CheckAvailable(map[string]int{"occupied": port})
	if err == nil {
		t.Fatal("a loopback-only listener went undetected")
	}
	if !strings.Contains(err.Error(), "occupied") {
		t.Errorf("error = %q, want it to name the occupied port", err)
	}
}

func TestNewRunDirLayout(t *testing.T) {
	base := t.TempDir()
	dir, err := NewRunDir(base)
	if err != nil {
		t.Fatalf("NewRunDir: %v", err)
	}

	for _, path := range []string{dir.Root, dir.Logs, dir.Databases} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", path)
		}
	}
	if filepath.Base(dir.Storage) != "storage" {
		t.Errorf("Storage = %q, want a storage directory", dir.Storage)
	}
	if _, err := os.Stat(dir.Storage); err != nil {
		t.Errorf("storage directory was not created: %v", err)
	}
	if got := dir.StorageURL(); got != "file://"+dir.Storage {
		t.Errorf("StorageURL = %q, want a file:// URL for %q", got, dir.Storage)
	}
	if filepath.Base(dir.Logs) != "logs" || filepath.Base(dir.Databases) != "databases" {
		t.Errorf("unexpected sub-directory names: %s, %s", dir.Logs, dir.Databases)
	}
	if _, err := time.Parse(runTimestampLayout, filepath.Base(dir.Root)); err != nil {
		t.Errorf("run directory %q is not named for its timestamp: %v", filepath.Base(dir.Root), err)
	}
}

func TestNewRunDirLinksLatest(t *testing.T) {
	base := t.TempDir()
	dir, err := NewRunDir(base)
	if err != nil {
		t.Fatalf("NewRunDir: %v", err)
	}

	target, err := os.Readlink(filepath.Join(base, "latest"))
	if err != nil {
		t.Fatalf("readlink latest: %v", err)
	}
	if target != dir.Root {
		t.Errorf("latest -> %s, want %s", target, dir.Root)
	}
}

func TestPathsAreNamedPerService(t *testing.T) {
	dir := RunDir{Logs: "/logs", Databases: "/databases"}

	if got := dir.LogPath("backend"); got != "/logs/backend.json" {
		t.Errorf("LogPath = %q", got)
	}
	if got := dir.ConsolePath("backend"); got != "/logs/backend.console" {
		t.Errorf("ConsolePath = %q", got)
	}
	// One SQLite file per service: two processes must never contend on one
	// database lock.
	if got := dir.DatabasePath("backend"); got != "/databases/backend.db" {
		t.Errorf("DatabasePath = %q", got)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	base := t.TempDir()
	names := []string{
		"2026-01-01-00-00-00",
		"2026-01-02-00-00-00",
		"2026-01-03-00-00-00",
		"2026-01-04-00-00-00",
	}
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(base, name, "logs"), 0o750); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	Prune(base, 2)

	for _, gone := range names[:2] {
		if _, err := os.Stat(filepath.Join(base, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived pruning", gone)
		}
	}
	for _, kept := range names[2:] {
		if _, err := os.Stat(filepath.Join(base, kept)); err != nil {
			t.Errorf("%s was pruned but should have been kept", kept)
		}
	}
}

func TestPruneIgnoresUnrelatedEntries(t *testing.T) {
	// The "latest" symlink and anything not shaped like a run must survive.
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "2026-01-01-00-00-00"), 0o750); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(base, "notes"), 0o750); err != nil {
		t.Fatalf("create notes: %v", err)
	}

	Prune(base, 1)

	if _, err := os.Stat(filepath.Join(base, "notes")); err != nil {
		t.Error("an unrelated directory was pruned")
	}
}

func TestPruneWithNothingToDo(t *testing.T) {
	base := t.TempDir()
	Prune(base, 0)       // keep < 1 is a no-op
	Prune("/no/such", 5) // an unreadable base must not panic
}

func TestPrefixerLabelsEveryLine(t *testing.T) {
	var out strings.Builder
	p := prefixer{name: "backend", out: &out}

	n, err := p.Write([]byte("first\nsecond\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len("first\nsecond\n") {
		t.Errorf("n = %d, want the full input length", n)
	}

	got := out.String()
	if strings.Count(got, "backend   |") != 2 {
		t.Errorf("output = %q, want both lines labelled", got)
	}
}

func TestPrefixerLabelsPartialLine(t *testing.T) {
	// Output arrives in arbitrary chunks; a trailing fragment must not vanish.
	var out strings.Builder
	p := prefixer{name: "console", out: &out}

	if _, err := p.Write([]byte("no trailing newline")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "no trailing newline") {
		t.Errorf("output = %q, want the fragment written", got)
	}
}
