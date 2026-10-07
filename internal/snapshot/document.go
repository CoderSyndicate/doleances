package snapshot

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// messageDocument renders one doléance as Markdown with YAML front matter.
//
// A doléance is a text, so it is exported as a text file rather than as a row.
// That is what makes a package greppable and readable by somebody with no
// tools at all, which is the whole reason for exporting rather than backing up.
//
// The fields written here are an allowlist, deliberately. The message model
// also carries a deletion-token hash; enumerating what goes in, rather than
// excluding what stays out, means a field added to the model later is absent
// from the package until somebody decides otherwise.
func messageDocument(m models.Message) []byte {
	var b strings.Builder

	b.WriteString("---\n")
	yamlField(&b, "id", m.ID)
	// A doléance is dated by when it was written. There is no separate
	// occurrence date: the text is a snapshot of how somebody felt that day,
	// whatever age the thing behind it has.
	yamlField(&b, "date", m.CreatedAt.UTC().Format(time.RFC3339))
	if m.PublishedAt != nil {
		yamlField(&b, "published", m.PublishedAt.UTC().Format(time.RFC3339))
	}
	if m.Nickname != "" {
		yamlField(&b, "nickname", m.Nickname)
	}
	// The birth year is exported rather than a computed age: an age is only
	// true on the day it is read, and a package outlives the day it was made.
	if m.BirthYear != 0 {
		yamlField(&b, "birth_year", strconv.Itoa(m.BirthYear))
	}
	if m.Activity != "" {
		yamlField(&b, "activity", m.Activity)
	}
	if m.Language != "" {
		yamlField(&b, "language", m.Language)
	}
	if len(m.Subjects) > 0 {
		slugs := make([]string, 0, len(m.Subjects))
		for _, subject := range m.Subjects {
			slugs = append(slugs, subject.Slug)
		}
		yamlList(&b, "subjects", slugs)
	}
	if m.Location != nil {
		b.WriteString("location:\n")
		b.WriteString("  latitude: " + strconv.FormatFloat(m.Location.Latitude, 'f', -1, 64) + "\n")
		b.WriteString("  longitude: " + strconv.FormatFloat(m.Location.Longitude, 'f', -1, 64) + "\n")
		if m.Location.Label != "" {
			b.WriteString("  label: " + yamlValue(m.Location.Label) + "\n")
		}
		if m.Location.CountryCode != "" {
			b.WriteString("  country: " + yamlValue(m.Location.CountryCode) + "\n")
		}
	}
	b.WriteString("---\n\n")

	// The text is written verbatim, with nothing appended — not even a
	// trailing newline to tidy the file. Padding it would make a text that
	// ended with a newline and one that did not produce identical bytes, and
	// the reader could then never tell them apart. This package claims the
	// texts in it are unedited; that has to be true to the byte.
	b.WriteString(m.Text)
	return []byte(b.String())
}

func yamlField(b *strings.Builder, key, value string) {
	b.WriteString(key + ": " + yamlValue(value) + "\n")
}

func yamlList(b *strings.Builder, key string, values []string) {
	b.WriteString(key + ":\n")
	for _, value := range values {
		b.WriteString("  - " + yamlValue(value) + "\n")
	}
}

// yamlValue quotes whatever would otherwise change meaning. A nickname is free
// text and can hold a colon, a hash or a leading dash, any of which turns an
// unquoted scalar into something else.
func yamlValue(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, ":#\n\"'{}[]&*!|>%@`") || strings.TrimSpace(s) != s ||
		strings.HasPrefix(s, "-") {
		return strconv.Quote(s)
	}
	return s
}

// readme travels inside the package, because the copy that survives is the one
// that still explains itself after the site that produced it is gone.
func readme(m *Manifest) string {
	var b strings.Builder

	b.WriteString("# Doléances — " + string(m.Kind) + " snapshot\n\n")
	b.WriteString("Generated " + m.GeneratedAt.Format(time.RFC3339) + " by " + m.Generator + ".\n")
	b.WriteString("Package schema version " + strconv.Itoa(m.SchemaVersion) + ".\n\n")

	b.WriteString("## What this is\n\n")
	b.WriteString(`This is a register of grievances — *cahiers de doléances* — exported as
plain files so that it does not depend on the site that produced it. In 2019,
France collected some 218,000 contributions in about 20,000 town hall
registers; they were sealed for fifty years, while the same consultation's
online contributions were published and then left behind a server error. A
public record that exists in only one place is one budget line from being
unreadable. Hence this file.

**You are meant to keep this.** Copy it, mirror it, rehost it, feed it to
whatever you like. That is not a tolerated use, it is the purpose.
`)
	b.WriteString("\n## Privacy\n\n" + m.Notice + "\n")

	b.WriteString("\n## What is inside\n\n")
	b.WriteString("| path | contents |\n|---|---|\n")
	b.WriteString("| `" + FileManifest + "` | kind, schema version, generation time, record counts |\n")
	b.WriteString("| `" + FileSubjects + "` | the subject vocabulary messages are classified against |\n")
	b.WriteString("| `" + FileAliases + "` | the other spellings each subject is known by, across languages |\n")
	b.WriteString("| `" + DirMessages + "/<id>.md` | one doléance: YAML front matter, then the text as written |\n")
	b.WriteString("| `" + DirHistorical + "/<id>.json` | a historical passage with its source and translations |\n")
	if m.Kind == KindFull {
		b.WriteString("| `" + DirGroups + "/<id>.json` | a local action group and its memberships, with no person named |\n")
		b.WriteString("| `" + DirGroups + "/<id>/actions/<id>.json` | an action with its occurrences |\n")
		b.WriteString("| `" + DirAudit + "/<yyyy-mm>.jsonl` | the append-only audit log, one JSON object per line |\n")
		b.WriteString("| `" + DirSettings + "/<name>.json` | operator settings, credentials redacted |\n")
	}

	b.WriteString("\n### Counts\n\n")
	for _, key := range sortedKeys(m.Counts) {
		b.WriteString("- " + key + ": " + strconv.Itoa(m.Counts[key]) + "\n")
	}

	b.WriteString(`
## Reading it

Every file is text. ` + "`unzip`" + ` it and read it with anything — an editor, ` + "`grep`" +
		`, a script. A doléance is a Markdown file whose front matter holds the date,
the language, the assigned subjects and the pinned location, if the author gave
one; everything after the second ` + "`---`" + ` is what the person wrote, unedited.
Nobody may alter a doléance in this system, including its operator, so what you
have here is what was written.

## Restoring it

A doléance installation imports this package back into a database. The reader
is the writer in reverse and is round-trip tested, because an export nobody can
import is a backup with extra steps.

## A note on what is missing

Deletion tokens are not exported: they authorise editing and deletion, and a
copy of them would let whoever holds this file impersonate contributors on a
rehosted register. Unpublished material — pending revisions, submissions that
were dropped or rejected — is not here either. This is a copy of the register,
not of the queue.

**No account is in here, in either kind of package.** An account is a
credential store — a random handle, a set of public keys, a sheet of recovery
hashes — and exporting one would let whoever holds this file authenticate this
register's members somewhere else. For the same reason there are no sessions,
no push endpoints and no notifications: those are credentials and records of
what one person was told.

Participation is exported with the person removed. A membership says that
somebody was in a group, which is the whole of what a public copy should say
about it; the identifier saying *which* somebody is the join that would turn a
mirror into a way to follow a person from group to group.

And nothing anybody wrote privately is here — neither the messages sent to a
group's admins, nor what a reader set aside to come back to. A snapshot is a
copy of the register, not of somebody's correspondence or of their reading.

A doléance the author deleted is absent from every snapshot generated after
that deletion. Older copies in other people's hands are the price of a public
record, and the submission form says so before anybody writes a word.
`)
	return b.String()
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
