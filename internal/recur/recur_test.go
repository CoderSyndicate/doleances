package recur

import (
	"strings"
	"testing"
	"time"
)

func at(spec string) time.Time {
	when, err := time.Parse("2006-01-02 15:04", spec)
	if err != nil {
		panic(err)
	}
	return when
}

// TestFourthThursdayOfTheMonth is the rhythm that started this: a string on a
// page that nothing could turn into a date.
func TestFourthThursdayOfTheMonth(t *testing.T) {
	rule, err := Parse("FREQ=MONTHLY;BYDAY=4TH")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	start := at("2026-09-24 19:00") // itself a fourth Thursday
	want := []string{
		"2026-10-22 19:00", "2026-11-26 19:00", "2026-12-24 19:00",
		"2027-01-28 19:00", "2027-02-25 19:00",
	}

	after := start
	for _, expected := range want {
		next, ok := Next(rule, start, after)
		if !ok {
			t.Fatalf("no occurrence after %s", after)
		}
		if got := next.Format("2006-01-02 15:04"); got != expected {
			t.Fatalf("next = %s, want %s", got, expected)
		}
		if next.Weekday() != time.Thursday {
			t.Errorf("%s is a %s", next, next.Weekday())
		}
		after = next
	}
}

// TestTheLastFridayIsNotTheFifth. Some months have a fifth Friday and some do
// not, so "last" has to be its own thing or the meeting vanishes four times a
// year.
func TestTheLastFridayIsNotTheFifth(t *testing.T) {
	rule, err := Parse("FREQ=MONTHLY;BYDAY=-1FR")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	start := at("2026-01-01 18:30")
	after := start
	for _, expected := range []string{
		"2026-01-30 18:30", "2026-02-27 18:30", "2026-03-27 18:30",
		"2026-04-24 18:30", "2026-05-29 18:30",
	} {
		next, ok := Next(rule, start, after)
		if !ok {
			t.Fatalf("no occurrence after %s", after)
		}
		if got := next.Format("2006-01-02 15:04"); got != expected {
			t.Fatalf("next = %s, want %s", got, expected)
		}
		// The defining property: adding a week leaves the month.
		if next.AddDate(0, 0, 7).Month() == next.Month() {
			t.Errorf("%s is not the last Friday of its month", next)
		}
		after = next
	}
}

// TestAShortMonthIsSkippedNotClamped. A group that meets on the 31st does not
// meet on the 28th, and moving the meeting would send people out on the wrong
// day. The RFC skips; so do we.
func TestAShortMonthIsSkippedNotClamped(t *testing.T) {
	rule, err := Parse("FREQ=MONTHLY;BYMONTHDAY=31")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	start := at("2026-01-31 20:00")
	after := start
	for _, expected := range []string{
		"2026-03-31 20:00", // February skipped
		"2026-05-31 20:00", // April skipped
		"2026-07-31 20:00", // June skipped
		"2026-08-31 20:00",
	} {
		next, ok := Next(rule, start, after)
		if !ok {
			t.Fatalf("no occurrence after %s", after)
		}
		if got := next.Format("2006-01-02 15:04"); got != expected {
			t.Fatalf("next = %s, want %s", got, expected)
		}
		after = next
	}
}

// TestEverySecondThursdayIsCountedFromTheStart. An interval without an anchor
// is meaningless, and the anchor is DTSTART — not today, or the answer would
// change depending on when it was asked.
func TestEverySecondThursdayIsCountedFromTheStart(t *testing.T) {
	rule, err := Parse("FREQ=WEEKLY;INTERVAL=2;BYDAY=TH")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	start := at("2026-09-03 19:00") // a Thursday

	// Asked from three different moments, the series is the same series.
	for _, moment := range []string{"2026-09-03 19:01", "2026-09-10 12:00", "2026-09-16 08:00"} {
		next, ok := Next(rule, start, at(moment))
		if !ok {
			t.Fatalf("no occurrence after %s", moment)
		}
		if got := next.Format("2006-01-02 15:04"); got != "2026-09-17 19:00" {
			t.Errorf("asked at %s, next = %s, want 2026-09-17 19:00", moment, got)
		}
	}

	// And it stays on the odd Thursdays a year later rather than drifting.
	next, ok := Next(rule, start, at("2027-09-01 00:00"))
	if !ok {
		t.Fatal("no occurrence a year on")
	}
	if weeks := int(next.Sub(start).Hours() / (24 * 7)); weeks%2 != 0 {
		t.Errorf("%s is %d weeks after the start, which is not an even number", next, weeks)
	}
}

// TestTwiceAWeekIsOneRhythm — a group meeting Tuesdays and Thursdays is one
// action, not two.
func TestTwiceAWeekIsOneRhythm(t *testing.T) {
	rule, err := Parse("FREQ=WEEKLY;BYDAY=TU,TH")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	start := at("2026-09-01 18:00") // a Tuesday
	after := start
	for _, expected := range []string{
		"2026-09-03 18:00", "2026-09-08 18:00", "2026-09-10 18:00", "2026-09-15 18:00",
	} {
		next, ok := Next(rule, start, after)
		if !ok {
			t.Fatalf("no occurrence after %s", after)
		}
		if got := next.Format("2006-01-02 15:04"); got != expected {
			t.Fatalf("next = %s, want %s", got, expected)
		}
		after = next
	}
}

// TestNothingBeforeTheSeriesBegins.
func TestNothingBeforeTheSeriesBegins(t *testing.T) {
	rule, _ := Parse("FREQ=WEEKLY;BYDAY=TH")
	start := at("2026-09-24 19:00")

	next, ok := Next(rule, start, at("2026-01-01 00:00"))
	if !ok {
		t.Fatal("no occurrence at all")
	}
	if next.Before(start) {
		t.Errorf("next = %s, which is before the series begins", next)
	}
}

// TestTheClockComesFromTheStart, not from the rule: RRULE says nothing about
// what time of day a meeting happens, and inventing one would be a meeting
// nobody announced.
func TestTheClockComesFromTheStart(t *testing.T) {
	rule, _ := Parse("FREQ=MONTHLY;BYDAY=1MO")
	start := at("2026-09-07 20:15")

	next, ok := Next(rule, start, start)
	if !ok {
		t.Fatal("no occurrence")
	}
	if next.Hour() != 20 || next.Minute() != 15 {
		t.Errorf("next is at %02d:%02d, want the start's 20:15", next.Hour(), next.Minute())
	}
}

// TestEveryShapeTheFormCanProduceRoundTrips. The stored column is only
// trustworthy if writing and reading it agree.
func TestEveryShapeTheFormCanProduceRoundTrips(t *testing.T) {
	for _, value := range []string{
		"FREQ=WEEKLY;BYDAY=TH",
		"FREQ=WEEKLY;INTERVAL=2;BYDAY=TU,TH",
		"FREQ=WEEKLY;INTERVAL=4;BYDAY=MO,WE,FR",
		"FREQ=MONTHLY;BYDAY=1TU",
		"FREQ=MONTHLY;BYDAY=4TH",
		"FREQ=MONTHLY;BYDAY=-1FR",
		"FREQ=MONTHLY;BYMONTHDAY=15",
	} {
		rule, err := Parse(value)
		if err != nil {
			t.Errorf("Parse(%q): %v", value, err)
			continue
		}
		if got := rule.String(); got != value {
			t.Errorf("round trip: %q -> %q", value, got)
		}
	}
}

// TestWhatIsNotUnderstoodIsRefusedAndNamed. A rule half understood schedules a
// meeting on a day nobody chose, so anything outside the subset is an error
// that says which part — not a silently dropped field.
func TestWhatIsNotUnderstoodIsRefusedAndNamed(t *testing.T) {
	for value, expect := range map[string]string{
		"FREQ=YEARLY;BYMONTH=5":               "BYDAY",
		"FREQ=DAILY":                          "DAILY",
		"FREQ=WEEKLY;BYDAY=TH;COUNT=10":       "COUNT",
		"FREQ=WEEKLY;BYDAY=TH;UNTIL=2027":     "UNTIL",
		"FREQ=MONTHLY;BYDAY=TH;BYSETPOS=2":    "BYSETPOS",
		"FREQ=MONTHLY;BYDAY=5TH":              "position",
		"FREQ=MONTHLY":                        "BYDAY",
		"FREQ=WEEKLY":                         "BYDAY",
		"BYDAY=TH":                            "FREQ",
		"FREQ=MONTHLY;BYDAY=1TH;BYMONTHDAY=3": "not both",
		"FREQ=WEEKLY;BYDAY=XX":                "weekday",
		"FREQ=WEEKLY;INTERVAL=0;BYDAY=TH":     "INTERVAL",
	} {
		_, err := Parse(value)
		if err == nil {
			t.Errorf("Parse(%q) was accepted", value)
			continue
		}
		if !strings.Contains(err.Error(), expect) {
			t.Errorf("Parse(%q) said %q, which does not name %q", value, err, expect)
		}
	}
}

// TestThePrefixIsOptional: both spellings turn up, and refusing one would be
// pedantry a caller has to work around.
func TestThePrefixIsOptional(t *testing.T) {
	with, err := Parse("RRULE:FREQ=MONTHLY;BYDAY=4TH")
	if err != nil {
		t.Fatalf("Parse with prefix: %v", err)
	}
	without, err := Parse("FREQ=MONTHLY;BYDAY=4TH")
	if err != nil {
		t.Fatalf("Parse without prefix: %v", err)
	}
	if with.String() != without.String() {
		t.Errorf("%q and %q parsed differently", with, without)
	}
}

// TestAnImpossibleRuleTerminates. maxSteps is what stands between a malformed
// row and a request that never returns.
func TestAnImpossibleRuleTerminates(t *testing.T) {
	// Nothing the parser accepts is impossible, so this is built by hand —
	// which is exactly how a bad row would arrive from a restored snapshot.
	rule := Rule{Frequency: Weekly, Interval: 1}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := Next(rule, at("2026-01-01 00:00"), at("2026-01-01 00:00")); ok {
			t.Error("a rule with no weekday produced a date")
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Next did not terminate")
	}
}

// TestOnceAYear — an AGM, a commemoration, the anniversary of a closure.
func TestOnceAYear(t *testing.T) {
	rule, err := Parse("FREQ=YEARLY;BYMONTH=11;BYDAY=3SA")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	start := at("2026-01-01 14:00")
	after := start
	for _, expected := range []string{
		"2026-11-21 14:00", "2027-11-20 14:00", "2028-11-18 14:00",
	} {
		next, ok := Next(rule, start, after)
		if !ok {
			t.Fatalf("no occurrence after %s", after)
		}
		if got := next.Format("2006-01-02 15:04"); got != expected {
			t.Fatalf("next = %s, want %s", got, expected)
		}
		if next.Weekday() != time.Saturday || next.Month() != time.November {
			t.Errorf("%s is not a November Saturday", next)
		}
		after = next
	}
}

// TestAYearlyDateSkipsTheYearsThatLackIt. The 29th of February is the case
// that proves the search has to reach past a single year.
func TestAYearlyDateSkipsTheYearsThatLackIt(t *testing.T) {
	rule, err := Parse("FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	start := at("2026-01-01 18:00")
	next, ok := Next(rule, start, start)
	if !ok {
		t.Fatal("no occurrence at all")
	}
	if got := next.Format("2006-01-02"); got != "2028-02-29" {
		t.Errorf("next = %s, want the next leap year", got)
	}
}

// TestAMonthOnlyMeansSomethingYearly. BYMONTH on a monthly rule is a rhythm
// nobody can have meant, so it is refused rather than quietly dropped.
func TestAMonthOnlyMeansSomethingYearly(t *testing.T) {
	if _, err := Parse("FREQ=MONTHLY;BYMONTH=5;BYMONTHDAY=1"); err == nil {
		t.Error("a monthly rule naming a month was accepted")
	}
	if _, err := Parse("FREQ=YEARLY;BYMONTHDAY=1"); err == nil {
		t.Error("a yearly rule with no month was accepted")
	}
}

// TestYearlyRoundTrips.
func TestYearlyRoundTrips(t *testing.T) {
	for _, value := range []string{
		"FREQ=YEARLY;BYMONTH=11;BYDAY=3SA",
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29",
		"FREQ=YEARLY;BYMONTH=5;BYDAY=-1FR",
	} {
		rule, err := Parse(value)
		if err != nil {
			t.Errorf("Parse(%q): %v", value, err)
			continue
		}
		if got := rule.String(); got != value {
			t.Errorf("round trip: %q -> %q", value, got)
		}
	}
}
