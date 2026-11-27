package store

import "time"

// Recurrence frequency values from the documented recurrence rule object.
const (
	RecurYearly  = 0
	RecurMonthly = 1
	RecurWeekly  = 2
	RecurDaily   = 3
)

// NextOccurrence returns the first occurrence strictly after from. The search
// is bounded so a rule that can never match fails closed instead of spinning.
func NextOccurrence(rule RecurrenceRule, from time.Time) (time.Time, bool) {
	base, err := time.Parse(time.RFC3339, rule.Start)
	if err != nil {
		return time.Time{}, false
	}
	interval := rule.Interval
	if interval < 1 {
		interval = 1
	}
	// A rule anchored after `from` starts there; otherwise walk forward.
	cursor := base
	if cursor.Before(from) || cursor.Equal(from) {
		cursor = from
	}
	for i := 0; i < 4000; i++ {
		candidate := cursor.AddDate(0, 0, i)
		if !candidate.After(from) {
			continue
		}
		if matchesRecurrence(rule, base, candidate, interval) {
			return candidate, true
		}
	}
	return time.Time{}, false
}

func matchesRecurrence(rule RecurrenceRule, base, candidate time.Time, interval int) bool {
	switch rule.Frequency {
	case RecurDaily:
		if interval != 1 {
			return false
		}
		if len(rule.ByWeekday) == 0 {
			return true
		}
		for _, d := range rule.ByWeekday {
			if docWeekday(candidate) == d {
				return true
			}
		}
		return false
	case RecurWeekly:
		if len(rule.ByWeekday) != 1 || docWeekday(candidate) != rule.ByWeekday[0] {
			return false
		}
		// Whole weeks between the rule anchor and the candidate.
		days := int(candidate.Sub(startOfWeek(base)).Hours() / 24)
		return (days/7)%interval == 0
	case RecurMonthly:
		if len(rule.ByNWeekday) != 1 {
			return false
		}
		nw := rule.ByNWeekday[0]
		if docWeekday(candidate) != nw.Day {
			return false
		}
		nth := (candidate.Day()-1)/7 + 1
		if nth != nw.N {
			return false
		}
		months := (candidate.Year()-base.Year())*12 + int(candidate.Month()) - int(base.Month())
		return months%interval == 0
	case RecurYearly:
		if len(rule.ByMonth) != 1 || len(rule.ByMonthDay) != 1 {
			return false
		}
		if int(candidate.Month()) != rule.ByMonth[0] || candidate.Day() != rule.ByMonthDay[0] {
			return false
		}
		years := candidate.Year() - base.Year()
		return years%interval == 0
	}
	return false
}

// docWeekday maps time.Weekday to the documented Monday=0..Sunday=6 order.
func docWeekday(t time.Time) int {
	return (int(t.Weekday()) + 6) % 7
}

func startOfWeek(t time.Time) time.Time {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	offset := docWeekday(t)
	return t.AddDate(0, 0, -offset)
}
