package recur

import "time"

// Description is a rule broken into the pieces a sentence needs.
//
// The sentence itself is assembled by the caller, from its own catalogue, in
// the reader's language. This package knows about dates and the RFC; it does
// not know how to say "the fourth Thursday of the month" in fifty languages,
// and a rendering built here would have to.
type Description struct {
	// Key names the shape of the sentence: "recur.weekly",
	// "recur.weekly_interval", "recur.monthly_weekday", "recur.monthly_day".
	Key string

	// Interval is how many weeks between, when the key says so.
	Interval int

	// Weekdays are the days, as time.Weekday, for the caller to name.
	Weekdays []time.Weekday

	// Week is 1 to 4, or Last, for a monthly weekday rhythm.
	Week int

	// Day is the day of the month, for a date rhythm.
	Day int

	// Month is which month, for a yearly rhythm.
	Month int
}

// Describe breaks a rule into the parts a sentence is built from.
func Describe(rule Rule) Description {
	switch {
	case rule.Frequency == Weekly && rule.Interval > 1:
		return Description{Key: "recur.weekly_interval",
			Interval: rule.Interval, Weekdays: rule.Weekdays}

	case rule.Frequency == Weekly:
		return Description{Key: "recur.weekly", Weekdays: rule.Weekdays}

	case rule.Frequency == Yearly && rule.Week != 0:
		return Description{Key: "recur.yearly_weekday", Month: rule.Month,
			Week: rule.Week, Weekdays: []time.Weekday{rule.Weekday}}

	case rule.Frequency == Yearly:
		return Description{Key: "recur.yearly_day", Month: rule.Month, Day: rule.Day}

	case rule.Week != 0:
		return Description{Key: "recur.monthly_weekday",
			Week: rule.Week, Weekdays: []time.Weekday{rule.Weekday}}

	case rule.Day != 0:
		return Description{Key: "recur.monthly_day", Day: rule.Day}
	}
	return Description{}
}
