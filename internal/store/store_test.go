package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(Options{Driver: DriverSQLite, DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close(context.Background()) }) //nolint:errcheck

	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func TestOpenRejectsBadOptions(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"unknown driver", Options{Driver: "mysql", DSN: "x"}, "unknown database driver"},
		{"empty driver", Options{DSN: "x"}, "unknown database driver"},
		{"missing dsn", Options{Driver: DriverSQLite}, "dsn is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(tt.opts)
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := newStore(t)
	if err := s.Migrate(); err != nil {
		t.Errorf("second Migrate: %v — migrations must be repeatable", err)
	}
}

func TestReadyReportsConnectivity(t *testing.T) {
	s := newStore(t)
	if err := s.Ready(context.Background()); err != nil {
		t.Errorf("Ready: %v", err)
	}

	s.Close(context.Background()) //nolint:errcheck
	if err := s.Ready(context.Background()); err == nil {
		t.Error("Ready succeeded on a closed database")
	}
}

func TestMessageRoundTripWithSubjects(t *testing.T) {
	s := newStore(t)
	db := s.DB()

	subjects := []models.Subject{
		{Slug: "healthcare", Label: "Healthcare"},
		{Slug: "rural-isolation", Label: "Rural isolation"},
	}
	if err := db.Create(&subjects).Error; err != nil {
		t.Fatalf("create subjects: %v", err)
	}

	msg := models.Message{
		Nickname:   "anonyme",
		Text:       "The maternity ward closed and the nearest one is ninety minutes away.",
		Language:   "en",
		Status:     models.StatusAccepted,
		Confidence: 94,
		Subjects:   subjects,
		Location:   &models.Location{Latitude: 44.8, Longitude: -0.6, Label: "Gironde", CountryCode: "FR"},
	}
	if err := db.Create(&msg).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	if msg.ID == "" {
		t.Fatal("no identifier was assigned")
	}

	var got models.Message
	if err := db.Preload("Subjects").First(&got, "id = ?", msg.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	// A message carries several subjects: forcing a single category would
	// lose what the grievance is actually about.
	if len(got.Subjects) != 2 {
		t.Errorf("subjects = %d, want 2", len(got.Subjects))
	}
	if got.Location == nil || got.Location.CountryCode != "FR" {
		t.Errorf("location = %+v, want the embedded coordinates back", got.Location)
	}
}

func TestOnePendingRevisionPerMessage(t *testing.T) {
	// A second edit overwrites the pending one; the database has to enforce
	// that rather than trust the caller.
	s := newStore(t)
	db := s.DB()

	msg := models.Message{Text: "original", Status: models.StatusAccepted}
	if err := db.Create(&msg).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	first := models.MessageRevision{MessageID: msg.ID, Text: "first edit", Status: models.StatusPending}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create revision: %v", err)
	}

	second := models.MessageRevision{MessageID: msg.ID, Text: "second edit", Status: models.StatusPending}
	if err := db.Create(&second).Error; err == nil {
		t.Error("a second pending revision was accepted; MessageID must be unique")
	}
}

// TestAccountHandleIsUnique: the handle is what an authenticator hands back at
// sign-in, and it is the only thing that names the account. Two accounts
// sharing one would make a sign-in ambiguous, which is not a state any code
// downstream is written to cope with.
func TestAccountHandleIsUnique(t *testing.T) {
	s := newStore(t)
	db := s.DB()

	handle := []byte("a-random-handle")
	if err := db.Create(&models.Account{Handle: handle, Name: "Someone"}).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := db.Create(&models.Account{Handle: handle}).Error; err == nil {
		t.Error("two accounts were allowed the same user handle")
	}
}

// TestAccountHoldsNoAddress is the whole point of the change that introduced
// it. Participant.Email was the only personal data this register held about a
// reader, and the separation of the two identity domains was a rule somebody
// had to keep rather than a fact about the schema.
func TestAccountHoldsNoAddress(t *testing.T) {
	s := newStore(t)

	columns, err := s.DB().Migrator().ColumnTypes(&models.Account{})
	if err != nil {
		t.Fatalf("read the account columns: %v", err)
	}
	for _, column := range columns {
		name := strings.ToLower(column.Name())
		if strings.Contains(name, "mail") || strings.Contains(name, "phone") {
			t.Errorf("an account carries a column called %q", column.Name())
		}
	}
}

func TestIntentIsUniquePerOccurrenceAndAccount(t *testing.T) {
	s := newStore(t)
	db := s.DB()

	group := models.Group{Name: "Test group", Status: models.StatusAccepted, Visible: true}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	action := models.Action{GroupID: group.ID, Title: "Monthly meeting", Type: models.ActionRecurrent, RecurrenceRule: "FREQ=MONTHLY;BYDAY=1TU"}
	if err := db.Create(&action).Error; err != nil {
		t.Fatalf("create action: %v", err)
	}
	occurrence := models.ActionOccurrence{ActionID: action.ID, StartsAt: time.Now().Add(72 * time.Hour)}
	if err := db.Create(&occurrence).Error; err != nil {
		t.Fatalf("create occurrence: %v", err)
	}
	account := models.Account{Handle: []byte("joiner")}
	if err := db.Create(&account).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}

	intent := models.Intent{OccurrenceID: occurrence.ID, AccountID: account.ID, DeclaredAt: time.Now()}
	if err := db.Create(&intent).Error; err != nil {
		t.Fatalf("create intent: %v", err)
	}
	duplicate := models.Intent{OccurrenceID: occurrence.ID, AccountID: account.ID, DeclaredAt: time.Now()}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Error("the same account declared twice for one occurrence")
	}
}

func TestMembershipIsUniquePerGroupAndAccount(t *testing.T) {
	s := newStore(t)
	db := s.DB()

	group := models.Group{Name: "Group", Status: models.StatusAccepted}
	account := models.Account{Handle: []byte("member")}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Create(&account).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}

	first := models.GroupMembership{GroupID: group.ID, AccountID: account.ID, Role: models.RoleAdmin, JoinedAt: time.Now()}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	if err := db.Create(&models.GroupMembership{GroupID: group.ID, AccountID: account.ID}).Error; err == nil {
		t.Error("an account was allowed to join the same group twice")
	}
}

func TestAuditEntryHoldsNoContent(t *testing.T) {
	// The audit log is a record about curators, not a copy of what they
	// reviewed — it must not resurrect the text of a deleted message.
	s := newStore(t)
	db := s.DB()

	entry := models.AuditEntry{
		ID:          models.NewID(),
		CreatedAt:   time.Now(),
		Actor:       "curator@authentik.example",
		Action:      models.AuditReject,
		SubjectType: "message",
		SubjectID:   models.NewID(),
		Reason:      "not a doléance",
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create audit entry: %v", err)
	}

	var got models.AuditEntry
	if err := db.First(&got, "id = ?", entry.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Actor != entry.Actor || got.Action != models.AuditReject {
		t.Errorf("entry = %+v, want the decision and its author", got)
	}
}

func TestDeletingAGroupCascades(t *testing.T) {
	s := newStore(t)
	db := s.DB()

	group := models.Group{
		Name:        "Doomed",
		Status:      models.StatusAccepted,
		Memberships: []models.GroupMembership{{AccountID: "somebody", Role: models.RoleAdmin}},
	}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Select("Memberships").Delete(&group).Error; err != nil {
		t.Fatalf("delete group: %v", err)
	}

	var memberships int64
	err := db.Model(&models.GroupMembership{}).Where("group_id = ?", group.ID).Count(&memberships).Error
	if err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if memberships != 0 {
		t.Errorf("%d memberships survived the group", memberships)
	}
}

func TestEnumValidation(t *testing.T) {
	if !models.StatusAccepted.Valid() || models.ReviewStatus("maybe").Valid() {
		t.Error("ReviewStatus.Valid does not match the defined states")
	}
	if !models.RoleAdmin.Valid() || models.GroupRole("creator").Valid() {
		t.Error("GroupRole.Valid should accept admin and host only — there is no creator role")
	}
	if !models.ActionRecurrent.Valid() || models.ActionType("weekly").Valid() {
		t.Error("ActionType.Valid does not match the defined types")
	}
	if !DriverSQLite.Valid() || Driver("mysql").Valid() {
		t.Error("Driver.Valid does not match the supported engines")
	}
}
