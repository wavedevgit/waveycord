package api

import (
	"fmt"
	"time"

	"github.com/wavedevgit/waveycord/internal/store"
)

// validDailyWeekdaySets are the only by_weekday sets the documented daily
// recurrence rule accepts.
var validDailyWeekdaySets = [][]int{
	{0, 1, 2, 3, 4},
	{1, 2, 3, 4, 5},
	{6, 0, 1, 2, 3},
	{4, 5},
	{5, 6},
	{6, 0},
}

func weekdaySetAllowed(days []int) bool {
	for _, allowed := range validDailyWeekdaySets {
		if len(allowed) != len(days) {
			continue
		}
		match := true
		for i := range days {
			if days[i] != allowed[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// validateRecurrenceRule enforces the documented recurrence subset. Field
// errors are reported with the offending field name.
func validateRecurrenceRule(r *store.RecurrenceRule) (string, string) {
	if r == nil {
		return "", ""
	}
	if _, err := time.Parse(time.RFC3339, r.Start); err != nil {
		return "recurrence_rule", "start must be an ISO8601 timestamp"
	}
	// These fields are server-owned; clients cannot set them.
	if r.End != nil {
		return "recurrence_rule", "end cannot be set"
	}
	if r.Count != nil {
		return "recurrence_rule", "count cannot be set"
	}
	if len(r.ByYearDay) > 0 {
		return "recurrence_rule", "by_year_day cannot be set"
	}
	if r.Frequency < store.RecurYearly || r.Frequency > store.RecurDaily {
		return "recurrence_rule", "frequency must be 0-3"
	}
	interval := r.Interval
	if interval == 0 {
		interval = 1
		r.Interval = 1
	}
	if interval < 1 || interval > 2 {
		return "recurrence_rule", "interval must be 1 or 2"
	}
	if interval == 2 && r.Frequency != store.RecurWeekly {
		return "recurrence_rule", "interval can only exceed 1 for weekly events"
	}
	groups := 0
	if len(r.ByWeekday) > 0 {
		groups++
	}
	if len(r.ByNWeekday) > 0 {
		groups++
	}
	if len(r.ByMonth) > 0 || len(r.ByMonthDay) > 0 {
		groups++
	}
	if groups > 1 {
		return "recurrence_rule", "by_weekday, by_n_weekday and by_month are mutually exclusive"
	}
	if len(r.ByWeekday) > 0 {
		if r.Frequency != store.RecurDaily && r.Frequency != store.RecurWeekly {
			return "recurrence_rule", "by_weekday is only valid for daily and weekly events"
		}
		for _, d := range r.ByWeekday {
			if d < 0 || d > 6 {
				return "recurrence_rule", "by_weekday values must be 0-6"
			}
		}
		if r.Frequency == store.RecurWeekly && len(r.ByWeekday) != 1 {
			return "recurrence_rule", "weekly by_weekday must contain exactly one day"
		}
		if r.Frequency == store.RecurDaily && !weekdaySetAllowed(r.ByWeekday) {
			return "recurrence_rule", "by_weekday is not an allowed weekday set"
		}
	}
	if len(r.ByNWeekday) > 0 {
		if r.Frequency != store.RecurMonthly {
			return "recurrence_rule", "by_n_weekday is only valid for monthly events"
		}
		if len(r.ByNWeekday) != 1 {
			return "recurrence_rule", "by_n_weekday must contain exactly one entry"
		}
		nw := r.ByNWeekday[0]
		if nw.N < 1 || nw.N > 5 {
			return "recurrence_rule", "by_n_weekday n must be 1-5"
		}
		if nw.Day < 0 || nw.Day > 6 {
			return "recurrence_rule", "by_n_weekday day must be 0-6"
		}
	}
	if len(r.ByMonth) > 0 || len(r.ByMonthDay) > 0 {
		if r.Frequency != store.RecurYearly {
			return "recurrence_rule", "by_month is only valid for yearly events"
		}
		if len(r.ByMonth) != 1 || len(r.ByMonthDay) != 1 {
			return "recurrence_rule", "yearly events require one by_month and one by_month_day"
		}
		if r.ByMonth[0] < 1 || r.ByMonth[0] > 12 {
			return "recurrence_rule", "by_month must be 1-12"
		}
		if r.ByMonthDay[0] < 1 || r.ByMonthDay[0] > 31 {
			return "recurrence_rule", "by_month_day must be 1-31"
		}
		// Reject impossible dates such as February 30.
		if _, err := time.Parse("2006-01-02", fmt.Sprintf("2000-%02d-%02d", r.ByMonth[0], r.ByMonthDay[0])); err != nil {
			return "recurrence_rule", "by_month_day is not a valid date for by_month"
		}
	}
	if r.Frequency == store.RecurMonthly && len(r.ByNWeekday) == 0 {
		return "recurrence_rule", "monthly events require by_n_weekday"
	}
	if r.Frequency == store.RecurYearly && len(r.ByMonth) == 0 {
		return "recurrence_rule", "yearly events require by_month and by_month_day"
	}
	return "", ""
}
