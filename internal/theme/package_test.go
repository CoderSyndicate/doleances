package theme

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// buildPackage writes a zip with the given entries.
func buildPackage(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

const (
	minimalStructure = `{"dimension":{"radius":{"$type":"dimension","$value":"0"}}}`
	minimalColor     = `{"light":{"accent":{"$type":"color","$value":"#ff0000"}}}`
)

func TestReadPackage(t *testing.T) {
	data := buildPackage(t, map[string]string{
		"structure.json":          minimalStructure,
		"colors/parchment.json":   minimalColor,
		"colors/nuit.json":        minimalColor,
		"assets/background.webp":  "not really webp",
		"assets/type/serif.woff2": "not really a font",
	})

	pkg, err := ReadPackage(data)
	if err != nil {
		t.Fatalf("ReadPackage: %v", err)
	}
	if string(pkg.Structure) != minimalStructure {
		t.Errorf("structure = %q", pkg.Structure)
	}
	if got := pkg.ColorNames(); len(got) != 2 || got[0] != "nuit" || got[1] != "parchment" {
		t.Errorf("colourways = %v, want both, named and sorted", got)
	}
	if got := pkg.AssetNames(); len(got) != 2 || got[0] != "background.webp" || got[1] != "type/serif.woff2" {
		t.Errorf("assets = %v, want both files keyed by their path inside assets/", got)
	}
	if pkg.Assets["background.webp"].ContentType != "image/webp" {
		t.Errorf("content type = %q", pkg.Assets["background.webp"].ContentType)
	}
}

func TestReadPackageIgnoresArchiveClutter(t *testing.T) {
	// Archives made on a Mac carry a metadata folder nobody asked for.
	data := buildPackage(t, map[string]string{
		"colors/only.json":          minimalColor,
		"__MACOSX/._structure.json": "junk",
		"assets/.DS_Store":          "junk",
		"assets/logo.svg":           "<svg/>",
	})

	pkg, err := ReadPackage(data)
	if err != nil {
		t.Fatalf("ReadPackage: %v", err)
	}
	if got := pkg.AssetNames(); len(got) != 1 || got[0] != "logo.svg" {
		t.Errorf("assets = %v, want only the real file", got)
	}
}

func TestReadPackageRejects(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"no colourway", map[string]string{"structure.json": minimalStructure}, "no colourway"},
		{"stray file", map[string]string{"colors/a.json": minimalColor, "readme.md": "hi"}, "unexpected file"},
		{"nested colourway", map[string]string{
			"colors/deep/a.json": minimalColor,
		}, "single .json file"},
		{"executable asset", map[string]string{
			"colors/a.json": minimalColor, "assets/evil.js": "alert(1)",
		}, "unsupported asset"},
		{"stylesheet asset", map[string]string{
			"colors/a.json": minimalColor, "assets/extra.css": "body{}",
		}, "unsupported asset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadPackage(buildPackage(t, tt.files))
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestReadPackageRejectsTraversal(t *testing.T) {
	// A zip may carry paths that climb out of it; none may reach the store.
	data := buildPackage(t, map[string]string{
		"colors/a.json":    minimalColor,
		"../../etc/passwd": "root:x:0:0",
	})

	if _, err := ReadPackage(data); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Errorf("error = %v, want an unsafe-path refusal", err)
	}
}

func TestReadPackageRejectsRubbish(t *testing.T) {
	if _, err := ReadPackage([]byte("this is not a zip")); err == nil {
		t.Error("accepted something that is not a zip")
	}
}

func TestValidateAssetReferences(t *testing.T) {
	pkg := Package{Assets: map[string]Asset{"background.webp": {Path: "background.webp"}}}

	tests := []struct {
		name  string
		value string
		ok    bool
	}{
		{"package file", "url(assets/background.webp)", true},
		{"quoted", `url("assets/background.webp")`, true},
		{"data uri is self-contained", "url(data:image/gif;base64,R0lGOD)", true},
		{"no url at all", "1px solid", true},

		{"absolute external", "url(https://cdn.example.com/bg.png)", false},
		{"protocol relative", "url(//cdn.example.com/bg.png)", false},
		{"site absolute", "url(/etc/passwd)", false},
		{"climbing out", "url(assets/../../secret.png)", false},
		{"missing from package", "url(assets/nope.png)", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pkg.ValidateAssetReferences([]string{tt.value})
			if tt.ok && err != nil {
				t.Errorf("rejected a valid reference: %v", err)
			}
			if !tt.ok && err == nil {
				t.Error("accepted a reference that can reach outside the package")
			}
		})
	}
}

func TestRewriteAssetURLs(t *testing.T) {
	css := []byte(`:root { --bg-image: url(assets/background.webp); --other: url(data:image/gif;base64,AA); }`)

	got := string(RewriteAssetURLs(css, "/theme/files/revolution"))
	if !strings.Contains(got, "url(/theme/files/revolution/background.webp)") {
		t.Errorf("package reference was not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "url(data:image/gif;base64,AA)") {
		t.Errorf("a data uri was rewritten:\n%s", got)
	}
}

func TestRewriteLeavesForeignURLsAlone(t *testing.T) {
	// Nothing should reach rendering with an external url — validation refuses
	// it at import — but the rewriter must not invent a local path for one.
	css := []byte(`:root { --x: url(https://example.com/a.png); }`)

	if got := string(RewriteAssetURLs(css, "/theme/files/x")); !strings.Contains(got, "https://example.com/a.png") {
		t.Errorf("an external url was rewritten into a local path:\n%s", got)
	}
}
