// Package snapshot exports the register as a zip of readable files.
//
// The point is not backup. A register that can only be read on the server that
// hosts it disappears when that server does — quietly, without anybody
// deciding to remove it. A snapshot is the register in a form somebody else
// can hold: Markdown and JSON in a zip, openable with no tooling and no
// database, rehostable by anyone who cares to.
//
// Two kinds exist because the material is not uniform. The register itself is
// anonymous and can be handed to anybody; groups and actions are keyed by
// email address and cannot. See KindContributions and KindFull.
package snapshot

import (
	"fmt"
	"strings"
	"time"
)

// SchemaVersion is the shape of the package, written into the manifest.
// A package that cannot say what shape it is cannot be imported safely later.
const SchemaVersion = 1

// Kind distinguishes the public export from the operator's one.
type Kind string

const (
	// KindContributions is the public register: published doléances, their
	// subjects and locations, and the historical corpus. It carries no
	// personal data because the register carries none.
	KindContributions Kind = "contributions"

	// KindFull adds groups, actions, participants, audit and settings. Every
	// one of those is keyed by, or contains, an email address, so this kind is
	// personal data in bulk and never leaves the console.
	KindFull Kind = "full"
)

// Public reports whether a kind may be served to anybody who asks.
func (k Kind) Public() bool { return k == KindContributions }

// Valid reports whether k is a kind this package knows how to build.
func (k Kind) Valid() bool { return k == KindContributions || k == KindFull }

// ParseKind resolves a kind from a request path or query value.
func ParseKind(s string) (Kind, error) {
	k := Kind(strings.ToLower(strings.TrimSpace(s)))
	if !k.Valid() {
		return "", fmt.Errorf("unknown snapshot kind %q", s)
	}
	return k, nil
}

// RedactedSecret replaces every stored credential in a full snapshot.
//
// A restored installation starts unconfigured and says so, rather than
// carrying working credentials into a copy that may be less well protected
// than the original was.
const RedactedSecret = "__REDACTED__"

// Manifest describes a package. It is the first thing a reader — human or
// program — looks at, so it holds everything needed to decide what the rest of
// the files are.
type Manifest struct {
	Kind          Kind           `json:"kind"`
	SchemaVersion int            `json:"schema_version"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Generator     string         `json:"generator"`
	Counts        map[string]int `json:"counts"`

	// Notice states in the file itself whether this package contains personal
	// data, so that nobody has to infer it from the kind.
	Notice string `json:"notice"`
}

// noticeFor is the sentence written into the manifest and the README.
func noticeFor(k Kind) string {
	switch k {
	case KindContributions:
		return "Contains no personal data. The register is anonymous by construction: " +
			"no email addresses, no deletion tokens, no participant records. " +
			"This package is meant to be copied, mirrored and rehosted."
	case KindFull:
		return "CONTAINS PERSONAL DATA — email addresses of group participants, contacts " +
			"and hosts, and the audit log of curator identities. Handle as personal data: " +
			"do not publish, do not share, store it as carefully as the database it came from. " +
			"Credentials (the LLM API key) are redacted."
	}
	return ""
}

// Filename is the name a package is downloaded under.
func Filename(k Kind, at time.Time) string {
	return fmt.Sprintf("doleances-%s-%s.zip", k, at.UTC().Format("20060102T150405Z"))
}

// Paths inside a package. They are constants because the reader and the writer
// have to agree, and a typo in one of them is a package that imports empty.
const (
	FileManifest  = "MANIFEST.json"
	FileReadme    = "README.md"
	FileSubjects  = "subjects.json"
	FileAliases   = "subject-aliases.json"
	DirMessages   = "messages"
	DirHistorical = "historical"
	DirGroups     = "groups"
	DirAudit      = "audit"
	DirSettings   = "settings"
)
