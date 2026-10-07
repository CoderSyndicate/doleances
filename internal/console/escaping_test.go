package console

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// TestConsoleCodeNeverBuildsMarkupFromData is the same guard as the
// frontend's, and it matters more here.
//
// A curator reads submissions **before** anything has judged them: the queue
// is where hostile text is most certain to appear, and the reader is the one
// person holding accept and reject over the whole register. A stored XSS in
// the console is not a defaced page, it is somebody else deciding what the
// register publishes.
//
// Every queue card, spam sample and snapshot row is built with createElement
// and textContent. This fails the build if that changes.
func TestConsoleCodeNeverBuildsMarkupFromData(t *testing.T) {
	// The rule has no exception, not even for `innerHTML = ""`, which is
	// harmless. A rule with a carve-out invites the next person to decide
	// their case also qualifies; textContent clears an element just as well,
	// so the strict rule costs nothing and stays easy to follow.
	dangerous := regexp.MustCompile(
		`\.innerHTML\s*=|\.outerHTML\s*=|insertAdjacentHTML|document\.write|` +
			`\.html\s*\(|new Function|eval\s*\(`)

	var checked int
	err := fs.WalkDir(assets, "static", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		checked++

		source, err := fs.ReadFile(assets, path)
		if err != nil {
			return err
		}
		for _, match := range dangerous.FindAllString(string(source), -1) {
			t.Errorf("%s builds markup from data with %q — a curator reads text "+
				"nothing has vetted, so this is where it matters most",
				path, strings.TrimSpace(match))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static assets: %v", err)
	}
	// A guard that silently checked nothing would be worse than no guard.
	if checked == 0 {
		t.Fatal("no scripts were checked; the walk found nothing")
	}
}
