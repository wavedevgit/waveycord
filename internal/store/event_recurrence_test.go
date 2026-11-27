package store_test

import (
	"testing"
	"time"

	"github.com/wavedevgit/waveycord/internal/store"
)

func mustTime(t *testing.T, raw string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return v
}

func TestRecurrenceNextOccurrence(t *testing.T) {
	cases := []struct {
		name string
		rule store.RecurrenceRule
		from string
		want string
	}{
		{
			name: "every weekday",
			rule: store.RecurrenceRule{
				Start: "2026-01-05T18:00:00Z", Frequency: store.RecurDaily, Interval: 1,
				ByWeekday: []int{0, 1, 2, 3, 4},
			},
			from: "2026-01-09T18:00:00Z", // Friday
			want: "2026-01-12T18:00:00Z", // Monday
		},
		{
			name: "every other wednesday",
			rule: store.RecurrenceRule{
				Start: "2026-01-07T18:00:00Z", Frequency: store.RecurWeekly, Interval: 2,
				ByWeekday: []int{2},
			},
			from: "2026-01-07T18:00:00Z",
			want: "2026-01-21T18:00:00Z",
		},
		{
			name: "monthly fourth wednesday",
			rule: store.RecurrenceRule{
				Start: "2026-01-28T18:00:00Z", Frequency: store.RecurMonthly, Interval: 1,
				ByNWeekday: []store.NWeekday{{N: 4, Day: 2}},
			},
			from: "2026-01-28T18:00:00Z",
			want: "2026-02-25T18:00:00Z",
		},
		{
			name: "annually july 24",
			rule: store.RecurrenceRule{
				Start: "2026-07-24T18:00:00Z", Frequency: store.RecurYearly, Interval: 1,
				ByMonth: []int{7}, ByMonthDay: []int{24},
			},
			from: "2026-07-24T18:00:00Z",
			want: "2027-07-24T18:00:00Z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := store.NextOccurrence(tc.rule, mustTime(t, tc.from))
			if !ok {
				t.Fatalf("no occurrence")
			}
			if want := mustTime(t, tc.want); !got.Equal(want) {
				t.Fatalf("got %s want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}

	// An impossible rule fails closed instead of returning a wrong date.
	bad := store.RecurrenceRule{
		Start: "2026-01-01T00:00:00Z", Frequency: store.RecurYearly,
		ByMonth: []int{2}, ByMonthDay: []int{30},
	}
	if _, ok := store.NextOccurrence(bad, mustTime(t, "2026-01-01T00:00:00Z")); ok {
		t.Fatalf("impossible rule produced an occurrence")
	}
}
