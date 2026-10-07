package theme

import (
	"archive/zip"
	"bytes"
	"strconv"
	"strings"
	"testing"
)

// TestAReadmeDoesNotStopAPackageBeingUploadedAgain.
//
// The package is the same shape in both directions — that is the whole design,
// and it is what lets somebody download a theme, change a colour and send it
// back. add() refuses any file it does not recognise, so packing a README into
// a download without teaching the reader about it would mean every theme this
// console produced failed to upload until the person worked out which file to
// delete.
func TestAReadmeDoesNotStopAPackageBeingUploadedAgain(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		PackageReadmeFile:   PackageReadme("revolution"),
		"colors/nuit.json":  `{"light":{"accent":{"$type":"color","$value":"#ffffff"}}}`,
		"assets/paper.webp": "not really a webp, but the extension is what is checked",
	} {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	pkg, err := ReadPackage(buf.Bytes())
	if err != nil {
		t.Fatalf("a package carrying its own README would not read back: %v", err)
	}
	if len(pkg.Colors) != 1 {
		t.Errorf("colourways = %v", pkg.ColorNames())
	}
	if len(pkg.Assets) != 1 {
		t.Errorf("assets = %v", pkg.AssetNames())
	}
}

// TestTheReadmeSaysWhatTheCodeEnforces.
//
// A README listing the limits by hand is a second copy of them, and the way
// anybody finds out it drifted is an upload refused for a reason the
// documentation said could not happen. These are read from the constants, so
// this test is really asking whether that is still true.
func TestTheReadmeSaysWhatTheCodeEnforces(t *testing.T) {
	readme := PackageReadme("revolution")

	for _, want := range []string{
		PackageStructureFile,
		PackageColorDir + "/",
		PackageAssetDir + "/",
		strconv.Itoa(MaxPackageFiles),
		strconv.Itoa(MaxPackageColors),
		mib(MaxPackageBytes),
		mib(MaxAssetBytes),
		"revolution",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("the README does not mention %q", want)
		}
	}

	// Every accepted extension is named, so a designer never has to guess
	// whether their font will be taken.
	for extension := range packageAssetTypes {
		if !strings.Contains(readme, "`"+extension+"`") {
			t.Errorf("the README does not name the accepted extension %q", extension)
		}
	}

	// And it must not promise anything that executes.
	for _, refused := range []string{"`.js`", "`.css`", "`.html`"} {
		if strings.Contains(readme, refused) {
			t.Errorf("the README lists %s as an accepted asset; a theme carries no code", refused)
		}
	}
}

// TestTheReadmeExplainsTheRulesAPersonWouldOtherwiseMeetAsAnError.
//
// Each of these is a refusal somebody hits by doing something reasonable: a
// package with no colourway, a token pointing at a CDN, a theme that seemed to
// merge rather than replace.
func TestTheReadmeExplainsTheRulesAPersonWouldOtherwiseMeetAsAnError(t *testing.T) {
	readme := PackageReadme("revolution")

	for topic, phrase := range map[string]string{
		"one colourway is required":  "except one colourway",
		"url() must stay inside":     "url(" + PackageAssetDir + "/",
		"data: is allowed":           "url(data:",
		"uploading replaces":         "replaces",
		"the named slots stay put":   "named slots",
		"contrast is advisory":       "never block",
		"both modes in one file":     `"dark"`,
		"the structure has no modes": "no modes",
	} {
		if !strings.Contains(readme, phrase) {
			t.Errorf("the README does not explain %s (looked for %q)", topic, phrase)
		}
	}
}
