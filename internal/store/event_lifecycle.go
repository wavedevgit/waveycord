package store

import "time"

// AdvanceExternalEvents applies scheduled external-event transitions atomically.
// Conditional updates allow concurrent instance sweepers without double events.
func (s *Store) AdvanceExternalEvents(at time.Time) ([]ScheduledEvent, error) {
	// ponytail: scan active external events in memory; use a persisted keyset cursor
	// when scheduled-event volume makes one sweep exceed its interval.
	rows, err := s.DB.Query(`SELECT id FROM scheduled_events WHERE entity_type = 3 AND status IN (1,2) ORDER BY id`)
	if err != nil {
		return nil, mapErr(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	changed := []ScheduledEvent{}
	for _, id := range ids {
		event, err := s.ScheduledEvent(id)
		if err != nil {
			return changed, err
		}
		start, err := time.Parse(time.RFC3339, event.ScheduledStartTime)
		if err != nil {
			continue
		}
		next := event.Status
		if event.Status == 1 && !at.Before(start) {
			next = 2
		}
		if event.ScheduledEndTime != nil {
			end, err := time.Parse(time.RFC3339, *event.ScheduledEndTime)
			if err == nil && !at.Before(end) {
				next = 3
			}
		}
		if next == event.Status {
			continue
		}
		result, err := s.DB.Exec(`UPDATE scheduled_events SET status = ? WHERE id = ? AND status = ?`, next, id, event.Status)
		if err != nil {
			return changed, mapErr(err)
		}
		if n, err := result.RowsAffected(); err != nil {
			return changed, err
		} else if n == 0 {
			continue
		}
		event.Status = next
		changed = append(changed, event)
	}
	return changed, nil
}
