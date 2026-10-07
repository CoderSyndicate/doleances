package backend

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/models"
)

// changesNothingCached is every write that does not declare invalidates(), and
// why each is allowed not to.
//
// The point of the list is that it is a list. A write that makes a cached
// answer wrong and says nothing leaves the register serving yesterday's front
// page, and nothing about the handler looks wrong — so the test below refuses
// any write this file has not accounted for, and adding an endpoint means
// either declaring what it invalidates or writing down here why it does not.
var changesNothingCached = map[string]string{
	// Credentials and the account itself. An account is a handle, a set of
	// public keys and a chosen name, and none of that is in any cached family:
	// the register, the map, the corpus and the vocabulary are the same for
	// everybody, which is the property that lets them be cached at all.
	//
	// Deleting an account is in here for a reason worth stating: it anonymises
	// memberships and the messages written to groups, and neither is published.
	// A group's row carries no member count — see GroupItem — and its inbox is
	// never cached.
	"begin-signup":           "credentials",
	"finish-signup":          "credentials",
	"begin-signin":           "credentials",
	"finish-signin":          "credentials",
	"use-recovery-code":      "credentials",
	"sign-out":               "credentials",
	"rename-account":         "a name nothing public shows",
	"delete-account":         "anonymises what is not cached; no member count is published",
	"begin-add-passkey":      "credentials",
	"finish-add-passkey":     "credentials",
	"rename-passkey":         "credentials",
	"delete-passkey":         "credentials",
	"revoke-session":         "credentials",
	"reissue-recovery-codes": "credentials",
	"open-device-link":       "credentials",
	"approve-device-link":    "credentials",
	"refuse-device-link":     "credentials",
	"claim-device-link":      "credentials",
	"poll-device-link":       "credentials",
	"begin-link-passkey":     "credentials",
	"finish-link-passkey":    "credentials",

	// Account-scoped by construction, so they could never be cached here.
	//
	// Bookmarks are the interesting entry: a reader keeping a doléance does
	// change what a page shows them, and the page still cannot be stale,
	// because the mark was never in the cache. Nothing cached is per-reader —
	// that is the property the whole arrangement rests on — so the "did I keep
	// this" flag is computed on top of the cached register for a signed-in
	// caller. See markKept.
	"keep-text":              "one reader's own list, never cached",
	"release-text":           "one reader's own list, never cached",
	"subscribe-push":         "one device's endpoint",
	"unsubscribe-push":       "one device's endpoint",
	"read-notification":      "one account's own list",
	"read-all-notifications": "one account's own list",
	"delete-notification":    "one account's own list",

	// Private text between people who have no other way to reach each other,
	// and the group's inbox is on a management page that is not cached.
	"write-to-group":     "private text, never cached",
	"read-group-message": "private text, never cached",

	// A role decides what somebody may do, and nothing public shows it: the
	// group page answers the caller's own role outside the cache, because that
	// answer is about the caller.
	"set-member-role": "a permission, not a published fact",

	// How the console reaches its identity provider. Not a cached family, and
	// never could be: the register, the map, the corpus and the vocabulary are
	// the same for everybody, and this is about who may administer them.
	"save-auth-settings": "operator settings",

	// The operator's own settings, and the theme library, which has a
	// mechanism of its own in each web service.
	"save-llm-settings":      "operator settings",
	"clear-llm-key":          "operator settings",
	"test-llm":               "reads nothing of ours",
	"save-curation-settings": "operator settings",
	"set-active-theme":       "the theme overlay has its own cache",
	"save-theme":             "the theme overlay has its own cache",
	"delete-theme":           "the theme overlay has its own cache",
	"save-theme-asset":       "the theme overlay has its own cache",
	"delete-theme-asset":     "the theme overlay has its own cache",

	// The snapshot listing is read by an admin on a page nobody else opens.
	"create-snapshot": "an admin listing, not cached",
	"delete-snapshot": "an admin listing, not cached",

	// Answers that record a decision and publish nothing. A rejected entity
	// and a dismissed question both exist so a curator is not asked twice;
	// neither changes a word a reader sees. Unlinking two subjects changes the
	// shape of the filter tree, which listSubjects builds from relations it
	// reads fresh on every request.
	"reject-subject-entity":    "records a decision, publishes nothing",
	"dismiss-subject-question": "records a decision, publishes nothing",
	"unlink-subjects":          "the relations are read fresh, not cached",

	// The two deliberate ones.
	//
	// A submission is pending and invisible until something assesses it, and
	// what assesses it is the sweep, which drops the register itself.
	"submit-message": "nothing is published yet",

	// And a "me too" is the one write allowed to leave a cached answer behind
	// the truth. The count may trail the entry's lifetime in a listing, which
	// is acceptable because nothing is ranked by it and nobody memorised it.
	// The one place somebody is looking straight at the number — the doléance's
	// own page — is not cached at all. See getMessage.
	"like-message":    "a count that may trail; the permalink is uncached",
	"like-historical": "a count that may trail; the permalink is uncached",

	// A plea is a reader saying a refusal was wrong. Nothing cached shows a
	// dropped submission — the register, the map and the corpus are what is
	// cached, and all three hold published material only — and the page that
	// does show it is read straight through, because a refusal is held for
	// hours and somebody looking at it is deciding whether to object.
	"plead-for-dropped": "nothing published changes; the dropped sample is uncached",
}

// TestEveryWriteSaysWhatItMakesWrong is the test that stops the next endpoint
// from forgetting.
//
// Invalidation is the mechanism and the lifetime on each entry is only the
// backstop, so a write that drops nothing is not a slow page — it is a
// register that is wrong for as long as the entry lives, with nothing to show
// anything happened. The declaration sits on the operation where somebody
// adding a route is already reading, and this refuses the route that has
// neither a declaration nor a line in the list above.
func TestEveryWriteSaysWhatItMakesWrong(t *testing.T) {
	api := humago.New(http.NewServeMux(), huma.DefaultConfig("Doléances", "test"))
	a := &API{groups: true}
	registerEverything(a, api)

	var silent []string
	for path, item := range api.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil {
				continue
			}
			if _, declared := op.Metadata[invalidated]; declared {
				continue
			}
			if _, accounted := changesNothingCached[op.OperationID]; accounted {
				continue
			}
			silent = append(silent, op.Method+" "+path+" ("+op.OperationID+")")
		}
	}

	sort.Strings(silent)
	for _, route := range silent {
		t.Errorf("%s neither declares invalidates(...) nor is accounted for in changesNothingCached", route)
	}
}

// TestNothingIsAccountedForTwice keeps the list above honest in the other
// direction: an entry that also declares invalidates() is a stale note, and
// one naming a route that no longer exists is a note about nothing. Either way
// the next person reading it is being told something untrue.
func TestNothingIsAccountedForTwice(t *testing.T) {
	api := humago.New(http.NewServeMux(), huma.DefaultConfig("Doléances", "test"))
	a := &API{groups: true}
	registerEverything(a, api)

	found := map[string]*huma.Operation{}
	for _, item := range api.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Post, item.Put, item.Patch, item.Delete} {
			if op != nil {
				found[op.OperationID] = op
			}
		}
	}

	for id := range changesNothingCached {
		op, exists := found[id]
		if !exists {
			t.Errorf("changesNothingCached names %q, which is not a write route", id)
			continue
		}
		if _, declared := op.Metadata[invalidated]; declared {
			t.Errorf("%q both declares invalidates(...) and is excused in changesNothingCached", id)
		}
	}
}

// TestACacheIsAnOptimisationNotADependency: a backend assembled without one
// must answer identically and simply read the database every time.
//
// This is not hypothetical tidiness. It was a panic: the invalidation
// middleware called Drop on an API built in a test with no cache, and a write
// path that works only once somebody remembered to construct a cache is a
// write path that breaks in whichever tool forgets.
func TestACacheIsAnOptimisationNotADependency(t *testing.T) {
	var absent *cache.Cache

	absent.Drop(cache.Messages)
	absent.Put("k", cache.Messages, 1)
	absent.Clear()

	if _, found := absent.Get("k"); found {
		t.Error("a cache that holds nothing answered a question")
	}
	if stats := absent.Stats(); stats.Entries != 0 {
		t.Errorf("entries = %d, want 0", stats.Entries)
	}

	loaded := 0
	for range 3 {
		value, err := cache.Fetch(absent, "k", cache.Messages, func() (int, error) {
			loaded++
			return 7, nil
		})
		if err != nil || value != 7 {
			t.Fatalf("Fetch = %v, %v", value, err)
		}
	}
	// Every time, because there is nowhere to keep the answer.
	if loaded != 3 {
		t.Errorf("the loader ran %d times, want 3", loaded)
	}
}

// TestTheFilterListIsNotRewrittenByAReader covers the hazard of caching
// decoded values rather than bytes.
//
// listSubjects renames every subject into the reader's language and sorts by
// the result. The slice it renames came out of the cache, so writing into it
// would rewrite the stored answer for everybody — and two readers doing it at
// once is a data race over somebody's filter list. Run with -race, which is
// what makes the concurrent half of this test mean anything.
func TestTheFilterListIsNotRewrittenByAReader(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()

	// Two subjects, named one way in French and the other way round in German,
	// so a leaked rename shows up as the wrong word rather than as nothing.
	for _, label := range []string{"transports", "santé"} {
		subject := models.Subject{Label: label, Slug: label, MatchKey: label, Language: "fr"}
		if err := a.store.DB().Create(&subject).Error; err != nil {
			t.Fatalf("create subject: %v", err)
		}
		alias := models.SubjectAlias{
			SubjectID: subject.ID,
			Language:  "de",
			Label:     map[string]string{"transports": "Verkehr", "santé": "Gesundheit"}[label],
		}
		alias.MatchKey = alias.Label
		if err := a.store.DB().Create(&alias).Error; err != nil {
			t.Fatalf("create alias: %v", err)
		}
	}

	labels := func(language string) []string {
		out, err := a.listSubjects(ctx, &SubjectListInput{Language: language})
		if err != nil {
			t.Fatalf("listSubjects(%q): %v", language, err)
		}
		var names []string
		for _, entry := range out.Body.Subjects {
			names = append(names, entry.Label)
		}
		sort.Strings(names)
		return names
	}

	french := labels("fr")
	german := labels("de")

	// The German reader must not have taught the cache that "santé" is called
	// "Gesundheit" — and then the French reader must still get French.
	if again := labels("fr"); len(again) != len(french) || again[0] != french[0] || again[1] != french[1] {
		t.Errorf("the French list became %v after a German reader, was %v", again, french)
	}
	if german[0] == french[0] {
		t.Fatalf("the fixture proves nothing: both languages answered %v", german)
	}

	// And concurrently, which is the shape the race actually arrives in.
	var wg sync.WaitGroup
	for _, language := range []string{"fr", "de", "fr", "de", "fr", "de"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.listSubjects(ctx, &SubjectListInput{Language: language}); err != nil {
				t.Errorf("listSubjects(%q): %v", language, err)
			}
		}()
	}
	wg.Wait()
}
