package store

import (
	"encoding/hex"
	"encoding/json"

	"github.com/wavedevgit/waveycord/internal/mls"
)

// SaveMLSProposal persists a validated proposal message so commits that reference it
// by ref can still be verified after a restart or on another instance.
func (s *Store) SaveMLSProposal(channelID string, m mls.ProposalMessage) error {
	body, err := json.Marshal(m.Proposal)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO mls_proposals (ref, channel_id, proposer, proposal, signature, created_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(ref) DO NOTHING`,
		m.Ref, channelID, m.Proposer, string(body), hex.EncodeToString(m.Signature), now())
	return mapErr(err)
}

// MLSProposalsForChannel loads persisted proposal messages keyed by ref.
func (s *Store) MLSProposalsForChannel(channelID string) (map[string]mls.ProposalMessage, error) {
	rows, err := s.DB.Query(`SELECT ref, proposer, proposal, signature FROM mls_proposals WHERE channel_id = ?`, channelID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]mls.ProposalMessage{}
	for rows.Next() {
		var (
			ref, proposer, body, sig string
		)
		if err := rows.Scan(&ref, &proposer, &body, &sig); err != nil {
			return nil, mapErr(err)
		}
		var p mls.Proposal
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			continue
		}
		raw, err := hex.DecodeString(sig)
		if err != nil {
			continue
		}
		out[ref] = mls.ProposalMessage{Proposal: p, Proposer: proposer, Ref: ref, Signature: raw}
	}
	return out, mapErr(rows.Err())
}
