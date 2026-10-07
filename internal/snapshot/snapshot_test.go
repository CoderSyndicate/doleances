package snapshot

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// Values that must never appear in a contributions package. Each is planted
// in the fixture below, so a leak is a failing test rather than an incident.
const (
	secretTokenHash = "TOKENHASH-b3f1c2a4e5d6"
	secretCurator   = "curator@example.org"
	secretAPIKey    = "sk-SECRET-api-key"
	secretPending   = "PENDING REVISION TEXT"
	secretDropped   = "DROPPED SPAM TEXT"
	secretSession   = "SESSION-TOKEN-HASH"

	// secretAccountID is what a membership row would say about *which* person
	// came, and it is planted in one so that it can be asserted absent.
	//
	// A snapshot's whole claim about participation is "this many people were
	// in this group", and the identifier is the join that would turn a public
	// mirror into a way to follow somebody between groups. The store adapter
	// strips it; this is what notices if it ever stops.
	secretAccountID = "ACCOUNT-ID-9f2b"
)

// fixture is a Source holding one of everything, including every value that
// must not escape.
type fixture struct{}

func (fixture) PublishedMessages(context.Context) ([]models.Message, error) {
	published := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	return []models.Message{{
		Model:    models.Model{ID: "msg-1", CreatedAt: published},
		Nickname: "Une femme de Saint-Jean",
		Location: &models.Location{
			Latitude: 43.3883, Longitude: -1.6626,
			Label: "Saint-Jean-de-Luz", CountryCode: "FR",
		},
		Text:        "La maternité a fermé.\n\nIl faut désormais une heure de route.",
		Language:    "fr",
		Status:      models.StatusAccepted,
		Subjects:    []models.Subject{{Slug: "sante"}, {Slug: "ruralite"}},
		TokenHash:   secretTokenHash,
		PublishedAt: &published,
	}}, nil
}

func (fixture) SubjectAliases(context.Context) ([]models.SubjectAlias, error) {
	return []models.SubjectAlias{
		{SubjectID: "s1", Label: "Gesundheit", Language: "de", MatchKey: "gesundheit"},
	}, nil
}

func (fixture) Subjects(context.Context) ([]models.Subject, error) {
	return []models.Subject{
		{Model: models.Model{ID: "s1"}, Slug: "sante", Label: "Santé"},
		{Model: models.Model{ID: "s2"}, Slug: "ruralite", Label: "Ruralité"},
	}, nil
}

func (fixture) HistoricalTexts(context.Context) ([]models.HistoricalText, error) {
	return []models.HistoricalText{{
		Model: models.Model{ID: "h1"},
		Title: "Doléances du sexe",
		Text:  "Nous demandons...",
	}}, nil
}

func (fixture) Groups(context.Context) ([]models.Group, error) {
	return []models.Group{{
		Model: models.Model{ID: "grp-1"},
		Name:  "Groupe de Bayonne",
		// Planted unstripped on purpose. The store adapter is what removes the
		// account reference, exactly as it is what redacts the settings below,
		// so a Source hands over the real thing and the assertions catch a
		// builder that ever exports it.
		Memberships: []models.GroupMembership{
			{GroupID: "grp-1", AccountID: secretAccountID, Role: models.RoleAdmin},
		},
	}}, nil
}

func (fixture) Actions(context.Context) ([]models.Action, error) {
	return []models.Action{{
		Model:   models.Model{ID: "act-1"},
		GroupID: "grp-1",
		Title:   "Réunion mensuelle",
		Type:    models.ActionRecurrent,
	}}, nil
}

func (fixture) AuditEntries(context.Context) ([]models.AuditEntry, error) {
	return []models.AuditEntry{{
		ID:          "a1",
		CreatedAt:   time.Date(2026, 3, 4, 11, 0, 0, 0, time.UTC),
		Actor:       secretCurator,
		Action:      models.AuditAccept,
		SubjectType: "message",
		SubjectID:   "msg-1",
	}}, nil
}

func (fixture) Settings(context.Context) (map[string]any, error) {
	// Redaction happens in the store adapter, so what a Source hands over is
	// already redacted. The test plants the real secrets anyway: if the
	// builder ever reads settings for a contributions package, it fails here.
	return map[string]any{
		"llm": map[string]string{"api_key": secretAPIKey},
	}, nil
}

func build(t *testing.T, kind Kind) ([]byte, *Manifest) {
	t.Helper()

	var buf bytes.Buffer
	manifest, err := Build(context.Background(), &buf, fixture{}, Options{
		Kind:      kind,
		Now:       time.Date(2026, 9, 16, 9, 45, 0, 0, time.UTC),
		Generator: "doleances-test",
	})
	if err != nil {
		t.Fatalf("build %s: %v", kind, err)
	}
	return buf.Bytes(), manifest
}

// TestContributionsPackageLeaksNothing is the test the privacy claim rests on.
// It searches the raw bytes of the package — not the parsed records — so an
// accidental export through some other field is caught too.
func TestContributionsPackageLeaksNothing(t *testing.T) {
	data, _ := build(t, KindContributions)

	forbidden := map[string]string{
		secretTokenHash: "a deletion-token hash would let a package holder impersonate contributors",
		secretCurator:   "the audit log identifies curators",
		secretAPIKey:    "an API key is a credential",
		secretPending:   "a pending revision was never published",
		secretDropped:   "dropped submissions are not the register",
		secretSession:   "a session token is a credential",
		secretAccountID: "an account identifier would say which person was in a group",
	}

	for value, why := range forbidden {
		if bytes.Contains(data, []byte(value)) {
			t.Errorf("contributions package contains %q: %s", value, why)
		}
	}

	// Also assert on the decompressed content: a value could survive
	// compression in a form the raw scan misses.
	for name, content := range entries(t, data) {
		for value, why := range forbidden {
			if strings.Contains(content, value) {
				t.Errorf("contributions package entry %s contains %q: %s", name, value, why)
			}
		}
	}
}

// TestFullPackageCarriesOperatorData is the other side of the same coin: the
// full package is supposed to contain this material, and a "safety" change
// that quietly emptied it would be a silent data-loss bug.
func TestFullPackageCarriesOperatorData(t *testing.T) {
	data, manifest := build(t, KindFull)
	files := entries(t, data)

	for _, want := range []string{
		"groups/grp-1.json",
		"groups/grp-1/actions/act-1.json",
		"audit/2026-03.jsonl",
		"settings/llm.json",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("full package is missing %s", want)
		}
	}
	if !strings.Contains(files["audit/2026-03.jsonl"], secretCurator) {
		t.Error("full package should carry the audit log's curator identities")
	}
	if manifest.Counts["groups"] != 1 {
		t.Errorf("groups count = %d, want 1", manifest.Counts["groups"])
	}
	if !strings.Contains(manifest.Notice, "CONTAINS PERSONAL DATA") {
		t.Errorf("full manifest notice does not warn: %q", manifest.Notice)
	}
}

// TestContributionsPackageOmitsOperatorFiles proves the exclusion is by
// absence of files, not merely by absence of the planted strings.
func TestContributionsPackageOmitsOperatorFiles(t *testing.T) {
	data, manifest := build(t, KindContributions)

	for name := range entries(t, data) {
		for _, forbidden := range []string{DirGroups, DirAudit, DirSettings} {
			if strings.HasPrefix(name, forbidden+"/") {
				t.Errorf("contributions package contains %s, which belongs to the full kind", name)
			}
		}
	}
	if _, ok := manifest.Counts["groups"]; ok {
		t.Error("contributions manifest counts groups, which it does not export")
	}
	if !strings.Contains(manifest.Notice, "no personal data") {
		t.Errorf("contributions notice should say it is safe to share: %q", manifest.Notice)
	}
}

func TestPackageCarriesManifestAndReadme(t *testing.T) {
	data, manifest := build(t, KindContributions)
	files := entries(t, data)

	if _, ok := files[FileManifest]; !ok {
		t.Fatal("no manifest")
	}
	readme, ok := files[FileReadme]
	if !ok {
		t.Fatal("no README")
	}
	// The README has to explain the package without the site that made it.
	// Each of these is a claim the package has to make on its own, because the
	// site that produced it may be gone by the time anybody reads this: what
	// it is for, how to read it back, and — the three that are promises to the
	// people in it — that credentials, identities and private text are absent.
	for _, want := range []string{
		"rehost",
		"Restoring it",
		"Deletion tokens are not exported",
		"No account is in here",
		"with the person removed",
		"nothing anybody wrote privately is here",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README does not mention %q", want)
		}
	}
	if manifest.SchemaVersion != SchemaVersion {
		t.Errorf("schema version = %d, want %d", manifest.SchemaVersion, SchemaVersion)
	}
	if manifest.Counts["messages"] != 1 || manifest.Counts["subjects"] != 2 {
		t.Errorf("unexpected counts: %v", manifest.Counts)
	}
}

// TestRoundTrip proves the README's claim that a package can be read back.
func TestRoundTrip(t *testing.T) {
	data, _ := build(t, KindFull)

	pkg, err := Read(data)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if pkg.Manifest.Kind != KindFull {
		t.Errorf("kind = %q, want %q", pkg.Manifest.Kind, KindFull)
	}

	if len(pkg.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(pkg.Messages))
	}
	got := pkg.Messages[0]
	want, _ := fixture{}.PublishedMessages(context.Background())

	if got.ID != want[0].ID {
		t.Errorf("id = %q, want %q", got.ID, want[0].ID)
	}
	if got.Text != want[0].Text {
		t.Errorf("text = %q, want %q", got.Text, want[0].Text)
	}
	if got.Nickname != want[0].Nickname {
		t.Errorf("nickname = %q, want %q", got.Nickname, want[0].Nickname)
	}
	if got.BirthYear != want[0].BirthYear {
		t.Errorf("birth year = %d, want %d", got.BirthYear, want[0].BirthYear)
	}
	if got.Activity != want[0].Activity {
		t.Errorf("activity = %q, want %q", got.Activity, want[0].Activity)
	}
	if got.Language != want[0].Language {
		t.Errorf("language = %q, want %q", got.Language, want[0].Language)
	}
	if !got.CreatedAt.Equal(want[0].CreatedAt) {
		t.Errorf("date = %v, want %v", got.CreatedAt, want[0].CreatedAt)
	}
	if got.Location == nil {
		t.Fatal("location was lost")
	}
	if got.Location.Label != "Saint-Jean-de-Luz" || got.Location.CountryCode != "FR" {
		t.Errorf("location = %+v", got.Location)
	}
	if got.Location.Latitude != 43.3883 || got.Location.Longitude != -1.6626 {
		t.Errorf("coordinates = %v, %v", got.Location.Latitude, got.Location.Longitude)
	}
	if len(got.Subjects) != 2 || got.Subjects[0].Slug != "sante" {
		t.Errorf("subjects = %+v", got.Subjects)
	}
	// The aliases carry every merge a curator ever decided and every name
	// learned from Wikidata. A package without them restores a register that
	// has forgotten all of it and asks the same questions again.
	if len(pkg.Aliases) != 1 || pkg.Aliases[0].Label != "Gesundheit" {
		t.Errorf("aliases = %+v", pkg.Aliases)
	}

	// A round-tripped message must never come back carrying a token hash.
	if got.TokenHash != "" {
		t.Errorf("token hash survived the round trip: %q", got.TokenHash)
	}

	if len(pkg.AuditEntries) != 1 || pkg.AuditEntries[0].Actor != secretCurator {
		t.Errorf("audit entries = %+v", pkg.AuditEntries)
	}
	if len(pkg.Actions) != 1 || pkg.Actions[0].GroupID != "grp-1" {
		t.Errorf("actions = %+v", pkg.Actions)
	}
	if len(pkg.Groups) != 1 || pkg.Groups[0].Name != "Groupe de Bayonne" {
		t.Errorf("groups = %+v", pkg.Groups)
	}
}

// TestMessageDocumentSurvivesAwkwardText checks the front matter against the
// text people actually write: colons, quotes, leading dashes, newlines.
func TestMessageDocumentSurvivesAwkwardText(t *testing.T) {
	awkward := []string{
		`Jean: le "vrai"`,
		"- anonyme -",
		"#gilet_jaune",
		"  spaces  ",
		"{braces}",
	}

	for _, nickname := range awkward {
		message := models.Message{
			Model:    models.Model{ID: "m", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			Nickname: nickname,
			Text:     "Texte:\nligne deux",
		}
		parsed, err := parseMessageDocument(messageDocument(message))
		if err != nil {
			t.Fatalf("nickname %q: %v", nickname, err)
		}
		if parsed.Nickname != nickname {
			t.Errorf("nickname round trip: got %q, want %q", parsed.Nickname, nickname)
		}
		if parsed.Text != message.Text {
			t.Errorf("text round trip: got %q, want %q", parsed.Text, message.Text)
		}
	}
}

// TestTextFidelity pins the exact-bytes promise: a trailing newline is part of
// what somebody wrote, and must survive the round trip either way.
func TestTextFidelity(t *testing.T) {
	for _, text := range []string{
		"sans saut de ligne",
		"avec saut de ligne\n",
		"deux sauts\n\n",
		"---\nun faux delimiteur\n---",
	} {
		message := models.Message{
			Model: models.Model{ID: "m", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			Text:  text,
		}
		parsed, err := parseMessageDocument(messageDocument(message))
		if err != nil {
			t.Fatalf("text %q: %v", text, err)
		}
		if parsed.Text != text {
			t.Errorf("text round trip: got %q, want %q", parsed.Text, text)
		}
	}
}

func TestReadRejectsRubbish(t *testing.T) {
	if _, err := Read([]byte("not a zip")); err == nil {
		t.Error("reading a non-zip should fail")
	}

	// A zip with no manifest is not a package.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("messages/x.md")
	_, _ = f.Write([]byte("hello"))
	_ = zw.Close()

	if _, err := Read(buf.Bytes()); err == nil {
		t.Error("reading a zip without a manifest should fail")
	}
}

func TestParseKind(t *testing.T) {
	for _, in := range []string{"contributions", "CONTRIBUTIONS", " full "} {
		if _, err := ParseKind(in); err != nil {
			t.Errorf("ParseKind(%q): %v", in, err)
		}
	}
	if _, err := ParseKind("everything"); err == nil {
		t.Error("ParseKind should reject an unknown kind")
	}
	if KindFull.Public() {
		t.Error("the full kind must never be public")
	}
	if !KindContributions.Public() {
		t.Error("the contributions kind is the public one")
	}
}

func TestFilename(t *testing.T) {
	at := time.Date(2026, 9, 16, 9, 45, 0, 0, time.UTC)
	got := Filename(KindContributions, at)
	want := "doleances-contributions-20260916T094500Z.zip"
	if got != want {
		t.Errorf("Filename = %q, want %q", got, want)
	}
}

// entries decompresses a package into a map of path to content.
func entries(t *testing.T, data []byte) map[string]string {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open package: %v", err)
	}

	files := map[string]string{}
	for _, entry := range zr.File {
		rc, err := entry.Open()
		if err != nil {
			t.Fatalf("open %s: %v", entry.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close() //nolint:errcheck
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name, err)
		}
		files[entry.Name] = string(content)
	}
	return files
}
