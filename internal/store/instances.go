package store

import "time"

// RegisterInstance records this API instance for cluster-wide mixer ownership.
func (s *Store) RegisterInstance(id string) error {
	_, err := s.DB.Exec(`INSERT INTO cluster_instances (id, last_seen) VALUES (?,?)
		ON CONFLICT(id) DO UPDATE SET last_seen = excluded.last_seen`, id, now())
	return mapErr(err)
}

// LiveInstances lists instances seen within ttl, sorted by id.
func (s *Store) LiveInstances(ttl time.Duration) ([]string, error) {
	cutoff := now() - ttl.Milliseconds()
	rows, err := s.DB.Query(`SELECT id FROM cluster_instances WHERE last_seen >= ? ORDER BY id ASC`, cutoff)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, id)
	}
	return out, mapErr(rows.Err())
}

// PruneInstances removes stale instance rows.
func (s *Store) PruneInstances(ttl time.Duration) {
	cutoff := now() - ttl.Milliseconds()
	_, _ = s.DB.Exec(`DELETE FROM cluster_instances WHERE last_seen < ?`, cutoff)
}

// UnregisterInstance removes an instance on clean shutdown.
func (s *Store) UnregisterInstance(id string) error {
	_, err := s.DB.Exec(`DELETE FROM cluster_instances WHERE id = ?`, id)
	return mapErr(err)
}
