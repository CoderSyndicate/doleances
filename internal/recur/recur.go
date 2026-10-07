// Package recur turns a repeating rhythm into dates.
//
// A local group's meeting is announced as a rule — "the fourth Thursday of the
// month", "every second Tuesday" — and a rule is not a date. Something has to
// turn one into the other, and the question is where.
//
// **Not at render time.** A page that evaluated a rhythm every time somebody
// looked at it could not order by it, could not answer "what is on in the next
// thirty days", and would do the same arithmetic for every visitor. So the
// next occurrence is computed when the rule changes and stored beside it, and
// every listing then sorts on an ordinary indexed column.
//
// # The rule is an RRULE, and the form writes it
//
// The stored form is the recurrence rule of **RFC 5545**, the iCalendar
// standard: `FREQ=MONTHLY;BYDAY=4TH`. Nobody types that, and nobody is asked
// to — a form offers the choices and assembles the string. What the standard
// buys is that the data leaves this project intact: a calendar subscription is
// a serialisation away rather than a rewrite, and a rhythm stored as English
// prose would have been neither.
//
// # A deliberate subset
//
// Only what the form can produce is understood: weekly on one or more
// weekdays, and monthly on the nth weekday or on a day of the month. Anything
// else — BYSETPOS, BYMONTH, yearly, COUNT, UNTIL — is **refused at parse
// time and named**, rather than silently ignored. A rule that is half
// understood schedules a meeting on a day nobody chose, and the whole point of
// this package is that the date it produces can be trusted.
//
// # DTSTART carries the clock
//
// As in the RFC, the rule says nothing about when a series begins or what time
// of day it happens: that is DTSTART, which here is the action's own start.
// It is also what anchors an interval — "every second Thursday" is meaningless
// without a Thursday to count from.
//
// Times are the site's own, never converted. A meeting announced for 19:00 is
// 19:00 where it happens; rendering it in a reader's zone would tell somebody
// in Berlin to arrive at a Guéret meeting an hour late.
package recur

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Frequency is how often a rhythm comes round.
type Frequency string

const (
	// Weekly repeats on one or more weekdays, every Interval weeks.
	Weekly Frequency = "WEEKLY"

	// Monthly repeats once a month, on the nth weekday or on a date.
	Monthly Frequency = "MONTHLY"

	// Yearly repeats once a year, in one month, on the nth weekday or on a
	// date. An AGM, a commemoration, the anniversary of a closure.
	Yearly Frequency = "YEARLY"
)

// Valid reports whether the frequency is one this package evaluates.
func (f Frequency) Valid() bool { return f == Weekly || f == Monthly || f == Yearly }

// Last selects the last matching weekday of a month — BYDAY=-1FR.
//
// It exists because "the last Friday" is a real rhythm and "the fifth Friday"
// is not: only some months have one, so a group that meets on the last Friday
// would otherwise skip a month four times a year.
const Last = -1

// Rule is the subset of RFC 5545 recurrence this project understands.
type Rule struct {
	Frequency Frequency

	// Interval is every how many weeks or months. 1 when unset.
	Interval int

	// Weekdays are the days a weekly rhythm falls on — BYDAY=TU,TH. A group
	// that meets twice a week is one rhythm, not two actions.
	Weekdays []time.Weekday

	// Week and Weekday together select the nth weekday of a month:
	// BYDAY=4TH is Week 4, Weekday Thursday. Week is 1 to 4, or Last.
	Week    int
	Weekday time.Weekday

	// Day is the day of the month — BYMONTHDAY=15. Used when Week is zero.
	Day int

	// Month is which month a yearly rhythm falls in — BYMONTH=9. Meaningless
	// for the other frequencies, and refused there rather than ignored.
	Month int
}

// weekdayCodes are the RFC's two-letter names, in its own order.
var weekdayCodes = [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

func codeToWeekday(code string) (time.Weekday, bool) {
	for i, known := range weekdayCodes {
		if known == code {
			return time.Weekday(i), true
		}
	}
	return 0, false
}

// Parse reads a recurrence rule.
//
// It accepts the property value with or without its `RRULE:` prefix, because
// both spellings turn up in the wild and refusing one would be pedantry a
// caller has to work around.
func Parse(value string) (Rule, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.ToUpper(value), "RRULE:")
	if value == "" {
		return Rule{}, fmt.Errorf("recur: empty rule")
	}

	rule := Rule{Interval: 1}
	var sawFreq, sawByDay bool

	for _, part := range strings.Split(value, ";") {
		if part == "" {
			continue
		}
		name, argument, found := strings.Cut(part, "=")
		if !found {
			return Rule{}, fmt.Errorf("recur: %q is not name=value", part)
		}

		switch name {
		case "FREQ":
			rule.Frequency = Frequency(argument)
			if !rule.Frequency.Valid() {
				return Rule{}, fmt.Errorf("recur: FREQ=%s is not supported; this "+
					"project understands WEEKLY, MONTHLY and YEARLY", argument)
			}
			sawFreq = true

		case "INTERVAL":
			interval, err := strconv.Atoi(argument)
			if err != nil || interval < 1 || interval > 52 {
				return Rule{}, fmt.Errorf("recur: INTERVAL=%s is not a count of weeks "+
					"or months between 1 and 52", argument)
			}
			rule.Interval = interval

		case "BYDAY":
			if err := parseByDay(&rule, argument); err != nil {
				return Rule{}, err
			}
			sawByDay = true

		case "BYMONTHDAY":
			day, err := strconv.Atoi(argument)
			if err != nil || day < 1 || day > 31 {
				return Rule{}, fmt.Errorf("recur: BYMONTHDAY=%s is not a day of the "+
					"month; negative days are not supported", argument)
			}
			rule.Day = day

		case "BYMONTH":
			month, err := strconv.Atoi(argument)
			if err != nil || month < 1 || month > 12 {
				return Rule{}, fmt.Errorf("recur: BYMONTH=%s is not a month", argument)
			}
			rule.Month = month

		case "WKST":
			// Which day a week starts on changes nothing this package
			// computes, so it is accepted and ignored rather than refused.

		default:
			// Named rather than skipped. A rule that is half understood
			// schedules a meeting on a day nobody chose.
			return Rule{}, fmt.Errorf("recur: %s is not supported; this project "+
				"understands FREQ, INTERVAL, BYDAY, BYMONTHDAY and BYMONTH", name)
		}
	}

	if !sawFreq {
		return Rule{}, fmt.Errorf("recur: the rule has no FREQ")
	}
	switch rule.Frequency {
	case Weekly:
		if !sawByDay || len(rule.Weekdays) == 0 {
			return Rule{}, fmt.Errorf("recur: a weekly rule needs BYDAY")
		}
		if rule.Week != 0 {
			return Rule{}, fmt.Errorf("recur: a weekly rule cannot count weeks of a month")
		}
	case Monthly:
		if rule.Month != 0 {
			return Rule{}, fmt.Errorf("recur: a monthly rule cannot name a month")
		}
		fallthrough
	case Yearly:
		if rule.Frequency == Yearly && rule.Month == 0 {
			return Rule{}, fmt.Errorf("recur: a yearly rule needs BYMONTH")
		}
		if rule.Week == 0 && rule.Day == 0 {
			return Rule{}, fmt.Errorf("recur: the rule needs BYDAY=nTH or BYMONTHDAY")
		}
		if rule.Week != 0 && rule.Day != 0 {
			return Rule{}, fmt.Errorf("recur: the rule takes BYDAY or BYMONTHDAY, not both")
		}
	}
	return rule, nil
}

func parseByDay(rule *Rule, argument string) error {
	for _, entry := range strings.Split(argument, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// An ordinal prefix — 1TH, -1FR — selects one weekday of a month.
		code := entry
		ordinal := 0
		if len(entry) > 2 {
			prefix := entry[:len(entry)-2]
			code = entry[len(entry)-2:]

			parsed, err := strconv.Atoi(prefix)
			if err != nil {
				return fmt.Errorf("recur: BYDAY=%s has no readable position", entry)
			}
			// 5TH is refused rather than treated as "last": most months have
			// no fifth Thursday, and a rhythm that vanishes four times a year
			// is not what anybody meant. -1 is how the RFC says "last".
			if parsed < 1 || parsed > 4 {
				if parsed != Last {
					return fmt.Errorf("recur: BYDAY=%s — a position is 1 to 4, or -1 "+
						"for the last", entry)
				}
			}
			ordinal = parsed
		}

		weekday, ok := codeToWeekday(code)
		if !ok {
			return fmt.Errorf("recur: BYDAY=%s is not a weekday", entry)
		}

		if ordinal != 0 {
			if rule.Week != 0 {
				return fmt.Errorf("recur: BYDAY takes one positioned weekday, not several")
			}
			rule.Week, rule.Weekday = ordinal, weekday
			continue
		}
		rule.Weekdays = append(rule.Weekdays, weekday)
	}

	sort.Slice(rule.Weekdays, func(i, j int) bool { return rule.Weekdays[i] < rule.Weekdays[j] })
	return nil
}

// String renders the rule back as an RFC 5545 property value.
//
// Round-tripping is what makes the stored column trustworthy: a form builds a
// rule, this writes it, Parse reads it back, and the two agree. The test says
// so for every shape the form can produce.
func (r Rule) String() string {
	parts := []string{"FREQ=" + string(r.Frequency)}
	if r.Interval > 1 {
		parts = append(parts, "INTERVAL="+strconv.Itoa(r.Interval))
	}
	if r.Month != 0 {
		parts = append(parts, "BYMONTH="+strconv.Itoa(r.Month))
	}

	switch {
	case r.Frequency == Weekly:
		codes := make([]string, 0, len(r.Weekdays))
		for _, weekday := range r.Weekdays {
			codes = append(codes, weekdayCodes[weekday])
		}
		parts = append(parts, "BYDAY="+strings.Join(codes, ","))

	case r.Week != 0:
		parts = append(parts, "BYDAY="+strconv.Itoa(r.Week)+weekdayCodes[r.Weekday])

	case r.Day != 0:
		parts = append(parts, "BYMONTHDAY="+strconv.Itoa(r.Day))
	}
	return strings.Join(parts, ";")
}

// maxSteps bounds the search.
//
// A rule that matches nothing would otherwise spin for ever, and one that
// matches rarely is real: BYMONTHDAY=31 skips February, April, June,
// September and November, so a search has to walk several months and still
// stop. Four hundred covers thirty years of monthly steps.
const maxSteps = 400

// Next returns the first occurrence strictly after `after`.
//
// `start` is DTSTART: when the series begins, what time of day it happens, and
// what an interval is counted from. Nothing before it is ever returned — a
// fortnightly meeting that began last month is on its own rhythm, not on one
// counted backwards from today.
//
// The bool is false when the rule produces nothing in range, which is not an
// error worth a caller's attention: an action whose rhythm yields no date
// simply has none, and a listing puts it last.
func Next(rule Rule, start, after time.Time) (time.Time, bool) {
	if !rule.Frequency.Valid() {
		return time.Time{}, false
	}
	if rule.Interval < 1 {
		rule.Interval = 1
	}
	// Nothing before the series begins, whatever is asked for.
	if after.Before(start) {
		after = start.Add(-time.Nanosecond)
	}

	switch rule.Frequency {
	case Weekly:
		return nextWeekly(rule, start, after)
	case Monthly:
		return nextMonthly(rule, start, after)
	case Yearly:
		return nextYearly(rule, start, after)
	}
	return time.Time{}, false
}

func nextWeekly(rule Rule, start, after time.Time) (time.Time, bool) {
	if len(rule.Weekdays) == 0 {
		return time.Time{}, false
	}

	// Intervals are counted in whole weeks from the week DTSTART falls in, so
	// "every second Thursday" lands on the same Thursdays however long ago the
	// series began and whenever it is asked about.
	weekStart := start.AddDate(0, 0, -int(start.Weekday()))
	weekStart = time.Date(weekStart.Year(), weekStart.Month(), weekStart.Day(),
		0, 0, 0, 0, start.Location())

	// Jump most of the way rather than stepping a week at a time, so a rhythm
	// announced years ago is found at once.
	elapsed := int(after.Sub(weekStart).Hours() / (24 * 7))
	if elapsed < 0 {
		elapsed = 0
	}
	week := weekStart.AddDate(0, 0, (elapsed/rule.Interval)*rule.Interval*7)

	for range maxSteps {
		for _, weekday := range rule.Weekdays {
			candidate := atClock(week.AddDate(0, 0, int(weekday)), start)
			if candidate.After(after) && !candidate.Before(start) {
				return candidate, true
			}
		}
		week = week.AddDate(0, 0, rule.Interval*7)
	}
	return time.Time{}, false
}

func nextMonthly(rule Rule, start, after time.Time) (time.Time, bool) {
	month := time.Date(after.Year(), after.Month(), 1, 0, 0, 0, 0, start.Location())

	for range maxSteps {
		if when, ok := inMonth(rule, month, start); ok &&
			when.After(after) && !when.Before(start) {
			return when, true
		}
		month = month.AddDate(0, 1, 0)
	}
	return time.Time{}, false
}

// nextYearly walks years rather than months: one month in twelve matters, and
// stepping through the other eleven would be eleven wasted comparisons a year
// against a search that already has to reach decades for BYMONTHDAY=29.
func nextYearly(rule Rule, start, after time.Time) (time.Time, bool) {
	year := after.Year()

	for range maxSteps {
		month := time.Date(year, time.Month(rule.Month), 1, 0, 0, 0, 0, start.Location())
		if when, ok := inMonth(rule, month, start); ok &&
			when.After(after) && !when.Before(start) {
			return when, true
		}
		year++
	}
	return time.Time{}, false
}

// inMonth places the rule inside one month, and reports whether that month has
// such a day at all.
//
// It can genuinely fail: the 31st does not exist in February, and the fourth
// Monday does not exist in a 28-day month beginning on a Tuesday.
func inMonth(rule Rule, month, start time.Time) (time.Time, bool) {
	days := daysIn(month)

	if rule.Week == 0 {
		if rule.Day > days {
			// Skipped rather than clamped: a group that meets on the 31st does
			// not meet on the 28th, and quietly moving the meeting would send
			// people out on the wrong day. This is what the RFC does too.
			return time.Time{}, false
		}
		return atClock(time.Date(month.Year(), month.Month(), rule.Day,
			0, 0, 0, 0, month.Location()), start), true
	}

	if rule.Week == Last {
		last := time.Date(month.Year(), month.Month(), days, 0, 0, 0, 0, month.Location())
		back := (int(last.Weekday()) - int(rule.Weekday) + 7) % 7
		return atClock(last.AddDate(0, 0, -back), start), true
	}

	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, month.Location())
	forward := (int(rule.Weekday) - int(first.Weekday()) + 7) % 7
	day := 1 + forward + (rule.Week-1)*7
	if day > days {
		return time.Time{}, false
	}
	return atClock(first.AddDate(0, 0, day-1), start), true
}

func daysIn(month time.Time) int {
	return time.Date(month.Year(), month.Month()+1, 0, 0, 0, 0, 0, month.Location()).Day()
}

// atClock puts DTSTART's time of day onto another day.
func atClock(day, start time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(),
		start.Hour(), start.Minute(), 0, 0, start.Location())
}
