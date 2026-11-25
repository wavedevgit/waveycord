package api

import "time"

// isoTime renders the API timestamp format.
func isoTime(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000000+00:00")
}

// isoTimePtr renders an optional timestamp.
func isoTimePtr(ms *int64) *string {
	if ms == nil {
		return nil
	}
	s := isoTime(*ms)
	return &s
}

func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
