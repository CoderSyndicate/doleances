package console

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// notInTheNavigation is every page this console serves that the navigation
// deliberately does not link to, each with the reason.
//
// The list is the point of the test below: a page may be left out, but leaving
// one out has to be a sentence somebody wrote rather than something nobody
// noticed.
// routesBuiltFromConstants are the files allowed to register a path this test
// cannot read, each with the reason it has to.
//
// The check below exists because the regex above matches a literal: a path
// built by concatenation is simply absent from the list and passes by never
// being looked at, which is a way to serve a page no test has an opinion
// about. These two are the cases where a constant is the right answer anyway.
var routesBuiltFromConstants = map[string]string{
	"signin.go": "the callback path is written into the provider's redirect URI " +
		"at provisioning time, so it is one constant shared with the auth package " +
		"rather than a string in two places",
	"wizard.go": "the setup page is on the maintenance mux, not this one, and is " +
		"deliberately not a page of the console at all",
}

var notInTheNavigation = map[string]string{
	"/static/": "assets, not a page",
	"/flags/":  "the language selector's flags, not a page",

	"/favicon.ico":                      "the tab icon, asked for by browsers rather than linked",
	"/apple-touch-icon.png":             "the same, for a home screen",
	"/apple-touch-icon-precomposed.png": "the same, for older iOS",

	"/theme/tokens.css":             "the overlay stylesheet",
	"/theme/assets/{slot}":          "an image the layout asks for by slot",
	"/theme/download/{name}":        "a download started from the theme page",
	"/theme/files/{name}/{path...}": "files a theme's own tokens reference",

	"/snapshots/download/{tag}": "a download started from the snapshots page",
	"/subjects/{id}":            "reached from the vocabulary list, not the bar",
}

// TestEveryPageIsReachableFromTheNavigation.
//
// A page can be routed, templated, translated, tested and completely invisible,
// because nothing connects registering a route to putting a link in the bar.
// That is exactly what happened to `/settings/people`: the whole people page
// worked and no console ever showed it.
//
// Nothing could fail — which is why this reads the two sides and compares them,
// the same way the register's card template is held against the script that
// redraws it.
func TestEveryPageIsReachableFromTheNavigation(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package: %v", err)
	}

	// A GET route registered on a mux, which is how every page here is declared.
	route := regexp.MustCompile(`"GET (/[^"]*)"`)

	var pages []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, found := range route.FindAllStringSubmatch(string(raw), -1) {
			path := found[1]
			// The API the console's own scripts call, rather than a page.
			if strings.HasPrefix(path, "/api/") {
				continue
			}
			pages = append(pages, path)
		}
	}
	if len(pages) == 0 {
		t.Fatal("no routes were found, so this test proves nothing")
	}

	// A route this test cannot read is a route it cannot vouch for, and the
	// failure is silent: the regex above matches a literal, so a path built by
	// concatenation is simply absent from `pages` and passes by not being
	// looked at. This caught exactly that, once — the flags route was
	// registered as "GET "+web.FlagsPath and no test had an opinion about it.
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, allowed := routesBuiltFromConstants[name]; allowed {
			continue
		}
		if strings.Contains(string(raw), `"GET "+`) {
			t.Errorf("%s registers a route by concatenation, which this test cannot read — "+
				"write the path as a literal, or name it in routesBuiltFromConstants "+
				"with the reason", name)
		}
	}

	layout, err := assets.ReadFile("templates/shared/layout.html")
	if err != nil {
		t.Fatalf("read the layout: %v", err)
	}
	bar := string(layout)

	for _, path := range pages {
		if _, exempt := notInTheNavigation[path]; exempt {
			continue
		}
		// The queue is the console's root and is linked as "/".
		if path == "/{$}" {
			path = "/"
		}
		if !strings.Contains(bar, `"Href" "`+path+`"`) {
			t.Errorf("%s is served and nothing links to it — "+
				"add it to the navigation, or to notInTheNavigation with the reason", path)
		}
	}
}

// TestTheNavigationLinksNothingThatIsNotServed.
//
// The other direction, and the one a reader meets as a 404: a link left behind
// after a page was renamed or removed.
func TestTheNavigationLinksNothingThatIsNotServed(t *testing.T) {
	layout, err := assets.ReadFile("templates/shared/layout.html")
	if err != nil {
		t.Fatalf("read the layout: %v", err)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package: %v", err)
	}
	var source strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		raw, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		source.Write(raw)
	}
	served := source.String()

	link := regexp.MustCompile(`"Href" "([^"]*)"`)
	found := link.FindAllStringSubmatch(string(layout), -1)
	if len(found) == 0 {
		t.Fatal("no navigation links were found, so this test proves nothing")
	}

	for _, entry := range found {
		href := entry[1]
		want := `"GET ` + href + `"`
		if href == "/" {
			want = `"GET /{$}"`
		}
		if !strings.Contains(served, want) {
			t.Errorf("the navigation links %s and nothing serves it", href)
		}
	}
}
