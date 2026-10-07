package store

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// portableColumnTypes are the explicit column types this schema is allowed to
// pin, because both engines have them under the same name.
//
// **Adding to this list means having checked both engines**, not having checked
// that the development database still starts. That is the whole point of the
// test below.
var portableColumnTypes = map[string]bool{
	// Both PostgreSQL and SQLite have `text`, unbounded, under that name.
	"text": true,
}

// TestNoColumnTypeIsEngineSpecific is the test that was missing when a
// deployment crash-looped.
//
// Five fields pinned `type:blob`. SQLite has `blob`; PostgreSQL does not — its
// equivalent is `bytea` — so `AutoMigrate` died on its first statement with
// `type "blob" does not exist`, the backend never came up, and the frontend
// hung waiting on an API that was never going to answer. It looked like a
// frontend fault four layers from where it was written.
//
// Nothing caught it because `--database-driver` defaults to sqlite: every test
// and every development run exercised the one engine where `blob` is valid, and
// the schema only ever met PostgreSQL in a deployed environment. Migrate() is
// documented as having to "produce the same result on both engines" and that
// was a sentence rather than a property.
//
// # What this proves and what it does not
//
// It proves no field pins a type outside the list above, which is the exact
// class of fault that happened and the only one reachable without a database.
// It cannot prove the generated DDL is accepted — a constraint, an index on a
// type that cannot carry one, a default PostgreSQL parses differently. The CI
// job that runs AutoMigrate against a real PostgreSQL is what covers those;
// this is what fails in the second it takes somebody to write the tag.
//
// **Removing a `type:` tag is usually the fix, not adding to the list.** GORM
// maps a Go type to each dialect's own equivalent when nothing overrides it —
// `[]byte` becomes `bytea` on PostgreSQL and `blob` on SQLite — so the tag that
// broke this was not buying anything the driver would not have done correctly.
func TestNoColumnTypeIsEngineSpecific(t *testing.T) {
	for _, model := range schema() {
		walkColumns(t, reflect.TypeOf(model), "")
	}
}

// walkColumns descends through a model and its embedded structs, checking the
// column type each field pins.
//
// Embedded structs are followed because that is where several of these live:
// `Location` carries the coordinates for every table that has a place, and a
// check that only looked at top-level fields would miss anything declared once
// and reused everywhere — which is the field most worth getting right.
func walkColumns(t *testing.T, typ reflect.Type, path string) {
	t.Helper()

	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}

	for i := range typ.NumField() {
		field := typ.Field(i)
		where := field.Name
		if path != "" {
			where = path + "." + field.Name
		}

		if declared, ok := columnType(field.Tag.Get("gorm")); ok {
			if !portableColumnTypes[declared] {
				t.Errorf("%s pins type:%s, which is not known to exist on both engines.\n"+
					"    PostgreSQL and SQLite do not share every type name — `blob` is "+
					"SQLite's and `bytea` is PostgreSQL's — and a type only one of them "+
					"has kills AutoMigrate on its first statement in the engine that "+
					"does not.\n"+
					"    Dropping the tag is usually right: GORM already picks each "+
					"dialect's own equivalent. If the type really is portable, add it to "+
					"portableColumnTypes and say which engines you checked.",
					where, declared)
			}
		}

		// Into embedded and nested structs, but not into time.Time, which is a
		// struct the driver handles as a scalar.
		inner := field.Type
		for inner.Kind() == reflect.Pointer || inner.Kind() == reflect.Slice {
			inner = inner.Elem()
		}
		if inner.Kind() == reflect.Struct && inner != reflect.TypeOf(time.Time{}) {
			walkColumns(t, inner, where)
		}
	}
}

// columnType pulls the `type:` option out of a gorm tag, if it declares one.
func columnType(tag string) (string, bool) {
	for _, option := range strings.Split(tag, ";") {
		name, value, found := strings.Cut(option, ":")
		if found && strings.EqualFold(strings.TrimSpace(name), "type") {
			return strings.ToLower(strings.TrimSpace(value)), true
		}
	}
	return "", false
}

// TestTheGuardWouldHaveCaughtIt pins that the check above actually fires on the
// tag that caused the outage, rather than passing because it looks in the wrong
// place or parses the tag wrongly.
//
// A guard nobody has watched fail is a guard nobody should trust, and this one
// is cheap to watch: the five real fields are fixed, so the only way to see it
// bite is to hand it the shape they had.
func TestTheGuardWouldHaveCaughtIt(t *testing.T) {
	type brokenAgain struct {
		Embedding []byte `gorm:"type:blob"`
	}

	fake := &testing.T{}
	walkColumns(fake, reflect.TypeOf(&brokenAgain{}), "")

	if !fake.Failed() {
		t.Error("type:blob passed the check that exists to refuse it")
	}

	// And it does not fire on what the schema legitimately pins, or it would be
	// a test somebody deletes rather than one they trust.
	type stillFine struct {
		Reason   string    `gorm:"type:text"`
		Name     string    `gorm:"size:128;index"`
		Place    string    `gorm:"TYPE:TEXT"` // case is the driver's business
		Happened time.Time `gorm:"index"`
	}

	fine := &testing.T{}
	walkColumns(fine, reflect.TypeOf(&stillFine{}), "")

	if fine.Failed() {
		t.Error("the check refused a type both engines have")
	}
}
