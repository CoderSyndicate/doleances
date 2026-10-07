// Package backend is the service that owns the data.
//
// It holds the database, the LLM client and every write. The console and the
// frontend reach all of it through this service's API and hold credentials to
// nothing else, which is what lets them ship as near-empty images.
package backend

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/cli"
	"github.com/CoderSyndicate/doleances/internal/config"
	"github.com/CoderSyndicate/doleances/internal/geocode"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/passkey"
	"github.com/CoderSyndicate/doleances/internal/push"
	"github.com/CoderSyndicate/doleances/internal/ratelimit"
	"github.com/CoderSyndicate/doleances/internal/service"
	"github.com/CoderSyndicate/doleances/internal/storage"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/CoderSyndicate/doleances/internal/wikidata"
	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// Default ports follow the house convention: 8xxx public, 7xxx internal
// tools, 9xxx maintenance. Every service uses a distinct pair so all three run
// side by side in development.
// Here, the backend is not publicly exposed, so it sits in the 7xxx tools range.
const (
	DefaultAppPort         = 7300
	DefaultMaintenancePort = 9300
)

// What the signup limiter allows.
//
// Ten ceremonies at once and ten a minute sustained, per address. The burst
// matters more than the rate: the honest case is bursty — a cancelled
// fingerprint prompt, a second attempt, somebody else on the same wifi — and
// the hostile one is steady.
//
// **It bounds the *beginning* of a ceremony and nothing else.** Finishing one
// requires a challenge that a begin already paid for, so counting both would
// halve the real allowance for no gain; and the device-linking poll runs every
// two seconds by design, which any limit worth having would refuse.
const (
	signupBurst  = 10
	signupRefill = 6 * time.Second
)

// Definition describes the backend binary.
func Definition() cli.Definition {
	return cli.Definition{
		Name:  "backend",
		Title: "Doléances API",
		Short: "The doléances API, persistence and classification service",
		Long: "The backend owns the database, the LLM client and every write.\n" +
			"It is not exposed publicly: the console and the frontend reach it over its API.",
		DefaultAppPort:         DefaultAppPort,
		DefaultMaintenancePort: DefaultMaintenancePort,
		RegisterFlags: func(cmd *cobra.Command) {
			config.RegisterDatabaseFlags(cmd)
			// What the credentials in that database are encrypted with. Only
			// the backend registers it: it is the only service that holds them.
			config.RegisterSettingsKeyFlag(cmd)
			config.RegisterStorageFlags(cmd)
			config.RegisterGeocodeFlags(cmd)
			// How much of a doléance its stored excerpt keeps. It belongs
			// here rather than on the frontend because the excerpt is derived
			// on write, and the backend owns every write.
			config.RegisterCardFlags(cmd)
			config.RegisterLanguageFlags(cmd)
			config.RegisterSiteFlags(cmd)
			config.RegisterFeatureFlags(cmd)
			config.RegisterLLMFlags(cmd)
			// Where readers reach this register, which is what a passkey is
			// bound to, plus the VAPID pair notifications are signed with.
			config.RegisterAuthFlags(cmd)
		},
		Setup: Setup,
	}
}

// Command returns the backend's root command.
func Command(version, commit string) *cobra.Command {
	return cli.Build(Definition(), version, commit)
}

// API holds the backend's dependencies.
type API struct {
	store   *store.Store
	storage storage.Store

	// storageConfig carries the retention and the switch gating the export
	// that contains personal data.
	storageConfig config.Storage

	// generator identifies this build inside a snapshot's manifest, so a
	// package found in five years says what wrote it.
	generator string

	// wikidata resolves a subject label to a language-neutral identity. Nil
	// when the lookup is switched off, and every call site tolerates that: a
	// subject with no QID simply uses the layers below.
	wikidata *wikidata.Client

	// geocoder names the place a pinned point falls in. It is nil when
	// reverse geocoding is switched off, and every call site tolerates that:
	// a location without a name is still a location.
	geocoder geocode.Geocoder

	// siteURL is where the public frontend answers. Empty when nobody has
	// told this installation its own address, and every caller has to cope:
	// guessing one produces mail that looks right and goes nowhere.
	siteURL string

	// passkeys is this register's WebAuthn relying party. Nil when no public
	// origin could be resolved, and every call site answers 503 rather than
	// panicking: a register whose accounts do not work should say so.
	passkeys *passkey.Service

	// push delivers the tap on the shoulder. Nil when no VAPID keys are
	// configured, which is not a failure state — the notification list is the
	// channel and everything is written to it either way.
	push *push.Sender

	// pushKey is the VAPID public key a browser needs in order to subscribe.
	// Empty when push is off.
	pushKey string

	// cache holds the answers that are the same for every reader: the
	// register, the map, the corpus, the vocabulary.
	//
	// It is here rather than in the services that read, because this is the
	// one that owns every write — which is what lets it invalidate rather
	// than expire, and so keep publication immediate.
	//
	// **Nothing account-scoped is ever put in it.** See internal/cache.
	cache *cache.Cache

	// signups bounds how often one caller may start a ceremony.
	//
	// It is what is left of the Sybil barrier. An account used to require a
	// working mailbox, which is slow to acquire in bulk; an account now
	// requires a fingerprint and a second, so the only things between a script
	// and ten thousand accounts are the curation queue and this.
	signups *ratelimit.Limiter

	// development mirrors the --development flag. It now changes nothing about
	// groups at all: the confirmation link and the unrecoverable management
	// token it used to work around are both gone with email.
	development bool

	// groups is whether this deployment offers local action groups at all. A
	// register that only collects doléances switches them off and needs no
	// mail server, which is the whole reason the switch exists.
	groups bool

	// languages are the languages the register organises subjects in. They
	// decide which of an entity's names are written into the vocabulary when
	// a subject is given a Wikidata identity — and so which doléances are
	// recognised without a curator being asked anything.
	languages []string
}

// Setup opens the database, migrates it, and registers the backend's routes.
func Setup(svc *service.Service, common config.Common) error {
	dbConfig, err := config.LoadDatabase()
	if err != nil {
		return fmt.Errorf("database configuration: %w", err)
	}

	db, err := store.Open(store.Options{
		Driver:       store.Driver(dbConfig.Driver),
		DSN:          dbConfig.DSN,
		MaxOpenConns: dbConfig.MaxConns,
		SettingsKey:  dbConfig.SettingsKey,
	})
	if err != nil {
		return err
	}
	svc.OnShutdown(db.Close)

	if err := db.Migrate(); err != nil {
		return err
	}

	storageConfig, err := config.LoadStorage()
	if err != nil {
		return fmt.Errorf("storage configuration: %w", err)
	}

	objects, err := storage.Open(context.Background(), storageConfig.URL)
	if err != nil {
		return err
	}
	svc.OnShutdown(func(context.Context) error { return objects.Close() })

	if storageConfig.SnapshotFullEnabled {
		// The export that carries every participant's email address is on.
		// That is a legitimate operator choice and a loud one.
		log.Warn().Msg("full snapshots are enabled: exports will contain personal data")
	}

	geocodeConfig := config.LoadGeocode()
	var geocoder geocode.Geocoder
	if geocodeConfig.Enabled {
		// Every provider this deployment configured, in one queue.
		//
		// Nominatim needs no credential, so it answers whenever geocoding is
		// on at all. Everybody else is in the queue because a key for them was
		// configured and absent from it because one was not — one fact, no
		// second switch to disagree with it.
		providers := []geocode.Provider{
			geocode.NewNominatim(geocodeConfig.Endpoint,
				geocodeConfig.UserAgent(svc.Version())),
		}
		providers = append(providers, geocode.Keyed(geocodeConfig.Keys)...)

		service := geocode.NewService(geocodeConfig.Limits, providers...)
		geocoder = service
		// What each provider will allow today, said once at startup: it is the
		// number an operator needs when labels stop appearing in the evening.
		log.Info().
			Strs("providers", service.Providers()).
			Interface("remaining_today", service.Remaining()).
			Str("endpoint", geocodeConfig.Endpoint).
			Msg("reverse geocoding enabled")
	}

	// Read before the API is built so the startup line reports what this
	// deployment will actually teach the vocabulary. Left unassigned, the
	// field is nil and every translation lookup returns early — which the
	// compiler cannot see and which looks, in the logs, like nothing at all.
	registerLanguages := config.LoadRegisterLanguages()
	log.Info().Int("count", len(registerLanguages)).
		Strs("languages", registerLanguages).
		Msg("subjects will be learned in these languages")

	// Where readers reach this register. It decides the relying party id, and a
	// passkey is bound to that for ever: it cannot be migrated, because the
	// private halves live on people's devices. Resolved and logged at startup
	// so a wrong answer is visible before anybody registers against it.
	authConfig, err := config.LoadAuth()
	if err != nil {
		return fmt.Errorf("authentication configuration: %w", err)
	}

	passkeys, err := passkey.New(passkey.Options{
		ID:          authConfig.RPID,
		DisplayName: config.LoadSiteName(),
		Origins:     authConfig.Origins,
	})
	if err != nil {
		// Refused here rather than at the first sign-in. A bad relying party
		// id produces an error in somebody's browser that nothing on the
		// server sees, so the only symptom would be that nobody can sign in
		// and nothing is logged.
		return fmt.Errorf("passkeys: %w", err)
	}
	log.Info().Str("relying_party", authConfig.RPID).
		Strs("origins", authConfig.Origins).
		Msg("passkeys bound to this relying party — changing it invalidates every credential")

	// No keys is not a failure. The notification list is the channel and the
	// push is a tap on the shoulder, so a register without one still tells
	// everybody everything — just not before they next open the site.
	pushConfig := config.LoadPush()
	var sender *push.Sender
	if pushConfig.Configured() {
		sender, err = push.New(push.Options{
			PublicKey:  pushConfig.PublicKey,
			PrivateKey: pushConfig.PrivateKey,
			Subject:    pushConfig.Subject,
		})
		if err != nil {
			return fmt.Errorf("web push: %w", err)
		}
		if pushConfig.Subject == "" {
			// VAPID requires it, and it is how a provider tells an operator
			// their sending is broken rather than silently dropping it.
			log.Warn().Msg("no --push-subject is set: a push service has no way to reach this operator")
		}
		log.Info().Msg("web push enabled")
	} else {
		log.Info().Msg("web push is off: notifications are written to the list and not delivered")
	}

	api := &API{
		store:         db,
		cache:         cache.New(cache.DefaultTTL, cache.DefaultLimit),
		passkeys:      passkeys,
		push:          sender,
		pushKey:       pushConfig.PublicKey,
		signups:       ratelimit.New(signupBurst, signupRefill),
		wikidata:      wikidata.New("", wikidataAgent(geocodeConfig, svc.Version())),
		languages:     registerLanguages,
		siteURL:       config.LoadSiteURL(),
		development:   common.Development,
		groups:        config.GroupsEnabled(),
		storage:       objects,
		storageConfig: storageConfig,
		generator:     "doleances " + svc.Version(),
		geocoder:      geocoder,
	}
	// Identities written before a person had to confirm them are actively
	// wrong in the matching path — "solitary confinement" standing as the
	// English name of rural isolation — so they go on startup rather than
	// waiting for somebody to run a tool.
	// Fixed before anything writes, because the hook that derives an excerpt
	// reads it and a row derived under the wrong length would be wrong
	// silently.
	models.SetExcerptRunes(config.LoadExcerptRunes())

	// And the rows that already exist are brought into line: a column added to
	// a populated table starts empty, and a changed length leaves every row
	// cut to the old one. Writes only what differs, so the starts after this
	// one cost a read and nothing else.
	if moved, err := db.RefreshExcerpts(context.Background(), 0); err != nil {
		log.Error().Err(err).Msg("cannot re-derive the stored excerpts")
	} else if moved > 0 {
		log.Info().Int64("messages", moved).
			Msg("stored excerpts re-derived against the configured length")
	}

	if purged, err := db.PurgeUnconfirmedEntities(context.Background()); err != nil {
		log.Error().Err(err).Msg("cannot purge unconfirmed Wikidata identities")
	} else if purged > 0 {
		log.Warn().Int64("subjects", purged).
			Msg("removed Wikidata identities that no person had confirmed, and the names derived from them")
	}

	if err := api.seedThemes(context.Background()); err != nil {
		return err
	}
	if err := api.seedLLMSettings(context.Background(), config.LoadLLM()); err != nil {
		return err
	}
	if !api.groups {
		// Said once, at INFO, because "why is there no Around me page" is
		// otherwise answered by reading the deployment's flags.
		log.Info().Msg("local action groups are switched off")
	}
	if err := api.seedCorpus(context.Background()); err != nil {
		return err
	}
	api.registerRoutes(svc)

	// The backend is only ready while its database is: reporting ready with
	// an unreachable database would send it traffic it cannot serve.
	svc.SetReadinessCheck(db.Ready)

	// Retention that nothing enforces is not retention. The sweep is stopped
	// as part of shutdown so it cannot outlive the database it writes to.
	purgeCtx, stopPurge := context.WithCancel(context.Background())
	go api.purgeSpamPeriodically(purgeCtx)
	svc.OnShutdown(func(context.Context) error {
		stopPurge()
		return nil
	})

	// The assessment sweep drains the pending queue. It is stopped as part of
	// shutdown so it cannot outlive the database, and a submission it was
	// holding is returned to the queue by the next run rather than left in a
	// state nobody looks at.
	assessCtx, stopAssessing := context.WithCancel(context.Background())
	go api.assessSubmissions(assessCtx)
	go api.reclassifyUnclassified(assessCtx)
	svc.OnShutdown(func(context.Context) error {
		stopAssessing()
		return nil
	})
	return nil
}

func (a *API) registerRoutes(svc *service.Service) {
	// Authentication and per-caller bounds are middleware rather than a check
	// inside each handler: a route declares what it needs, and a route copied
	// from another cannot forget to enforce it.
	svc.API().UseMiddleware(a.authenticate(svc.API()))

	huma.Register(svc.API(), huma.Operation{
		OperationID: "list-subjects",
		Method:      http.MethodGet,
		Path:        "/v1/subjects",
		Summary:     "List subjects",
		Description: "The themes the classifier assigns to messages, and what the register and the " +
			"map filter on. Pass ?language= to get each one named in that language where " +
			"the vocabulary knows the word: the list is otherwise a mix of whichever " +
			"spellings happened to arrive first.",
		Tags: []string{"Subjects"},
	}, a.listSubjects)

	a.registerAuthRoutes(svc.API())
	a.registerAccountRoutes(svc.API())
	// Deliberately not behind the groups switch, like the account pages
	// themselves: keeping a doléance has nothing to do with whether this
	// deployment offers local groups.
	a.registerBookmarkRoutes(svc.API())
	a.registerLinkRoutes(svc.API())
	a.registerNotificationRoutes(svc.API())
	a.registerThemeRoutes(svc.API())
	a.registerThemeAssetRoutes(svc.API())
	a.registerLLMRoutes(svc.API())
	a.registerSpamRoutes(svc.API())
	a.registerDroppedRoutes(svc.API())
	a.registerHistoricalRoutes(svc.API())
	a.registerMessageRoutes(svc.API())
	a.registerCurationRoutes(svc.API())
	a.registerSubjectQuestionRoutes(svc.API())
	a.registerVocabularyRoutes(svc.API())
	// Not registered when groups are off, so the API says what the deployment
	// does rather than answering for a feature it does not have.
	if a.groups {
		a.registerGroupRoutes(svc.API())
		a.registerMembershipRoutes(svc.API())
		a.registerActionRoutes(svc.API())
	}
	a.registerSnapshotRoutes(svc.API())
}

// SubjectListInput selects the language the filter list is read in.
type SubjectListInput struct {
	// Language is the reader's, not the doléance's. A German reader browsing a
	// mostly-French register should still see "Gesundheit" on the filter that
	// a French contributor created as "santé" — it is one subject, and which
	// of its names is shown is a question about who is reading.
	Language string `query:"language" doc:"two-letter code; names each subject in that language where known"`
}

// SubjectFilterEntry is one row of the filter list.
type SubjectFilterEntry struct {
	models.Subject

	// Depth is how far below a root this subject sits, so a filter can show
	// the hierarchy by indenting rather than by drawing a tree. Zero for a
	// subject with no broader subject above it.
	Depth int `json:"depth"`
}

// SubjectsOutput is the response body of the subject listing.
type SubjectsOutput struct {
	Body struct {
		Subjects []SubjectFilterEntry `json:"subjects"`
	}
}

func (a *API) listSubjects(ctx context.Context, in *SubjectListInput) (*SubjectsOutput, error) {
	// Keyed on the language, because the names a reader sees are that
	// language's aliases: one cache entry per language the register speaks,
	// and most installations speak two or three in practice.
	held, err := cache.Fetch(a.cache, cache.Keyed("subjects.list", in.Language),
		cache.Subjects, func() ([]models.Subject, error) {
			return a.store.ListSubjects(ctx)
		})
	if err != nil {
		log.Error().Err(err).Msg("cannot list subjects")
		return nil, huma.Error500InternalServerError("cannot list subjects")
	}

	// Copied, because what follows renames every entry and reorders the slice,
	// and the slice came out of the cache: the same backing array is handed to
	// every reader asking this question. Writing into it would rewrite the
	// stored answer — and two readers doing it at once is a data race over
	// somebody's filter list. See the note on Cache.
	subjects := make([]models.Subject, len(held))
	copy(subjects, held)

	// The slug is untouched: it is the identity, it is in the query strings
	// people have bookmarked, and a filter that renamed itself per reader
	// would break every link between them.
	if labels, err := a.store.SubjectLabelsIn(ctx, in.Language); err != nil {
		log.Error().Err(err).Str("language", in.Language).
			Msg("cannot read the subject names in that language")
	} else {
		for i, subject := range subjects {
			if label, known := labels[subject.ID]; known {
				subjects[i].Label = label
			}
		}
		// Sorted after translating, because alphabetical order is a property
		// of the words a reader actually sees.
		sort.Slice(subjects, func(i, j int) bool {
			return subjects[i].Label < subjects[j].Label
		})
	}

	relations, err := a.store.AllSubjectRelations(ctx)
	if err != nil {
		// A filter list without its hierarchy is still a filter list. Losing
		// the indentation is a presentation problem; losing the subjects would
		// take the page down.
		log.Error().Err(err).Msg("cannot read the subject hierarchy, listing flat")
	}

	out := &SubjectsOutput{}
	out.Body.Subjects = arrangeAsTree(subjects, relations)
	return out, nil
}

// arrangeAsTree orders subjects so that each follows the subject it sits
// under, with a depth a filter can indent by.
//
// # One row per subject
//
// The hierarchy is a directed graph, not a tree: "public transport" is below
// both "transport service" and "public service", and both are true. A filter
// list cannot show that honestly — two checkboxes for one subject is two ways
// to select the same thing — so each subject appears exactly once, under
// whichever of its parents comes first alphabetically. The relation is not
// lost, only the second drawing of it.
//
// # Bounded
//
// Depth is capped and every subject is emitted at most once, so a cycle in the
// data produces a flat tail rather than a hang. The store refuses cycles, and
// a renderer that trusts that would be trusting the wrong thing: this runs on
// every page that offers a filter.
func arrangeAsTree(subjects []models.Subject, relations []models.SubjectRelation) []SubjectFilterEntry {
	const maxDepth = 6

	children := map[string][]string{}
	hasParent := map[string]bool{}
	known := make(map[string]models.Subject, len(subjects))
	for _, subject := range subjects {
		known[subject.ID] = subject
	}

	for _, relation := range relations {
		// A relation to a subject that is not in this listing tells us
		// nothing about how to draw it.
		if _, ok := known[relation.ChildID]; !ok {
			continue
		}
		if _, ok := known[relation.ParentID]; !ok {
			continue
		}
		children[relation.ParentID] = append(children[relation.ParentID], relation.ChildID)
		hasParent[relation.ChildID] = true
	}

	// Subjects arrive sorted by the label a reader sees, and every list built
	// from them keeps that order.
	byLabel := func(ids []string) {
		sort.Slice(ids, func(i, j int) bool {
			return known[ids[i]].Label < known[ids[j]].Label
		})
	}
	for parent := range children {
		byLabel(children[parent])
	}

	out := make([]SubjectFilterEntry, 0, len(subjects))
	emitted := make(map[string]bool, len(subjects))

	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		if emitted[id] || depth > maxDepth {
			return
		}
		emitted[id] = true
		out = append(out, SubjectFilterEntry{Subject: known[id], Depth: depth})
		for _, child := range children[id] {
			walk(child, depth+1)
		}
	}

	for _, subject := range subjects {
		if !hasParent[subject.ID] {
			walk(subject.ID, 0)
		}
	}
	// Anything a cycle kept out of the walk still belongs in the filter.
	for _, subject := range subjects {
		if !emitted[subject.ID] {
			out = append(out, SubjectFilterEntry{Subject: subject, Depth: 0})
		}
	}
	return out
}

// wikidataAgent identifies this deployment to Wikimedia.
//
// Their rate limits turn on this string: a client they can contact gets
// 200 requests a minute, one they cannot gets 10. The project URL alone
// satisfies the policy, and the operator's own contact — the same one
// Nominatim is given, since it is the same operator — is added when it is
// configured, because a person is easier to reach than a repository.
func wikidataAgent(geocodeConfig config.Geocode, version string) string {
	agent := "doleances/" + version + " (+https://github.com/CoderSyndicate/doleances"
	if geocodeConfig.Contact != "" {
		agent += "; " + geocodeConfig.Contact
	}
	return agent + ")"
}
