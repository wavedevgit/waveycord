package store

import (
	"database/sql"
	"strings"

	"github.com/wavedevgit/waveycord/internal/id"
)

// SKU is the documented premium offering object.
type SKU struct {
	ID             string   `json:"id"`
	Type           int      `json:"type"`
	ApplicationID  string   `json:"application_id"`
	Name           string   `json:"name"`
	Slug           string   `json:"slug"`
	Flags          int64    `json:"flags"`
	DependentSKUID *string  `json:"dependent_sku_id"`
	AccessType     int      `json:"access_type"`
	Features       []string `json:"features"`
	Premium        bool     `json:"premium"`
	ShowAgeGate    bool     `json:"show_age_gate"`
}

// Entitlement is the documented entitlement object.
type Entitlement struct {
	ID             string  `json:"id"`
	SKUID          string  `json:"sku_id"`
	ApplicationID  string  `json:"application_id"`
	UserID         *string `json:"user_id,omitempty"`
	GuildID        *string `json:"guild_id,omitempty"`
	Type           int     `json:"type"`
	Deleted        bool    `json:"deleted"`
	Consumed       *bool   `json:"consumed,omitempty"`
	StartsAt       *string `json:"starts_at"`
	EndsAt         *string `json:"ends_at"`
	SubscriptionID *string `json:"subscription_id,omitempty"`
}

// Subscription is the documented subscription object.
type Subscription struct {
	ID                 string   `json:"id"`
	UserID             string   `json:"user_id"`
	SKUIDs             []string `json:"sku_ids"`
	EntitlementIDs     []string `json:"entitlement_ids"`
	RenewalSKUIDs      []string `json:"renewal_sku_ids"`
	CurrentPeriodStart *string  `json:"current_period_start"`
	CurrentPeriodEnd   *string  `json:"current_period_end"`
	Status             int      `json:"status"`
	CanceledAt         *string  `json:"canceled_at"`
}

// ListSKUs returns every SKU for an application.
func (s *Store) ListSKUs(appID string) ([]SKU, error) {
	rows, err := s.DB.Query(`SELECT id, type, application_id, name, slug, flags
		FROM skus WHERE application_id = ? ORDER BY id`, appID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []SKU{}
	for rows.Next() {
		var sku SKU
		if err := rows.Scan(&sku.ID, &sku.Type, &sku.ApplicationID, &sku.Name, &sku.Slug, &sku.Flags); err != nil {
			return nil, mapErr(err)
		}
		sku.AccessType = 1
		sku.Features = []string{}
		out = append(out, sku)
	}
	return out, mapErr(rows.Err())
}

// CreateSKU inserts a premium offering. SKUs are normally created from the
// developer portal, which has no public API.
func (s *Store) CreateSKU(appID string, skuType int, name, slug string, flags int64) (SKU, error) {
	idv := s.NewID()
	_, err := s.DB.Exec(`INSERT INTO skus (id, application_id, type, name, slug, flags, created_at)
		VALUES (?,?,?,?,?,?,?)`, idv, appID, skuType, name, slug, flags, now())
	if err != nil {
		return SKU{}, mapErr(err)
	}
	return SKU{ID: idv, Type: skuType, ApplicationID: appID, Name: name, Slug: slug,
		Flags: flags, AccessType: 1, Features: []string{}}, nil
}

// EntitlementFilter is the documented entitlement query.
type EntitlementFilter struct {
	UserID         string
	GuildID        string
	SKUIDs         []string
	Before         string
	After          string
	Limit          int
	ExcludeEnded   bool
	ExcludeDeleted bool
}

// ListEntitlements returns an application's entitlements.
func (s *Store) ListEntitlements(appID string, f EntitlementFilter) ([]Entitlement, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 100
	}
	q := `SELECT id, sku_id, application_id, user_id, guild_id, type, deleted, consumed,
		starts_at, ends_at, subscription_id FROM entitlements WHERE application_id = ?`
	args := []any{appID}
	if f.UserID != "" {
		q += ` AND user_id = ?`
		args = append(args, f.UserID)
	}
	if f.GuildID != "" {
		q += ` AND guild_id = ?`
		args = append(args, f.GuildID)
	}
	if len(f.SKUIDs) > 0 {
		q += ` AND sku_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.SKUIDs)), ",") + `)`
		for _, sku := range f.SKUIDs {
			args = append(args, sku)
		}
	}
	if f.Before != "" {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	if f.After != "" {
		q += ` AND id > ?`
		args = append(args, f.After)
	}
	if f.ExcludeEnded {
		q += ` AND ends_at IS NULL`
	}
	if f.ExcludeDeleted {
		q += ` AND deleted = 0`
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Entitlement{}
	for rows.Next() {
		e, err := scanEntitlement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, mapErr(rows.Err())
}

// Entitlement loads one entitlement for an application.
func (s *Store) Entitlement(appID, idv string) (Entitlement, error) {
	row := s.DB.QueryRow(`SELECT id, sku_id, application_id, user_id, guild_id, type, deleted, consumed,
		starts_at, ends_at, subscription_id FROM entitlements
		WHERE application_id = ? AND id = ?`, appID, idv)
	return scanEntitlement(row)
}

// CreateTestEntitlement grants a SKU to a user or guild in perpetuity.
func (s *Store) CreateTestEntitlement(appID, skuID, ownerID string, ownerType int) (Entitlement, error) {
	var exists int
	if err := s.DB.QueryRow(`SELECT 1 FROM skus WHERE application_id = ? AND id = ?`, appID, skuID).Scan(&exists); err != nil {
		return Entitlement{}, mapErr(err)
	}
	idv := s.NewID()
	user, guild := sql.NullString{}, sql.NullString{}
	if ownerType == 1 {
		guild = sql.NullString{String: ownerID, Valid: true}
	} else {
		user = sql.NullString{String: ownerID, Valid: true}
	}
	_, err := s.DB.Exec(`INSERT INTO entitlements
		(id, application_id, sku_id, user_id, guild_id, type, deleted, consumed, created_at)
		VALUES (?,?,?,?,?,4,0,0,?)`, idv, appID, skuID, user, guild, now())
	if err != nil {
		return Entitlement{}, mapErr(err)
	}
	return s.Entitlement(appID, idv)
}

// ConsumeEntitlement marks a consumable entitlement used.
func (s *Store) ConsumeEntitlement(appID, idv string) error {
	res, err := s.DB.Exec(`UPDATE entitlements SET consumed = 1 WHERE application_id = ? AND id = ?`, appID, idv)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEntitlement soft-deletes an entitlement.
func (s *Store) DeleteEntitlement(appID, idv string) error {
	res, err := s.DB.Exec(`UPDATE entitlements SET deleted = 1 WHERE application_id = ? AND id = ?`, appID, idv)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSubscriptions returns the subscriptions containing a SKU, filtered by
// user when provided.
func (s *Store) ListSubscriptions(skuID, userID, before, after string, limit int) ([]Subscription, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	q := `SELECT id, user_id, sku_ids, entitlement_ids, renewal_sku_ids, current_period_start,
		current_period_end, status, canceled_at FROM subscriptions WHERE 1=1`
	args := []any{}
	if userID != "" {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	if before != "" {
		q += ` AND id < ?`
		args = append(args, before)
	}
	if after != "" {
		q += ` AND id > ?`
		args = append(args, after)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit*4)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		has := false
		for _, idv := range sub.SKUIDs {
			if skuID == "" || idv == skuID {
				has = true
			}
		}
		if has {
			out = append(out, sub)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, mapErr(rows.Err())
}

// Subscription loads one subscription scoped to a SKU.
func (s *Store) Subscription(skuID, idv string) (Subscription, error) {
	row := s.DB.QueryRow(`SELECT id, user_id, sku_ids, entitlement_ids, renewal_sku_ids,
		current_period_start, current_period_end, status, canceled_at
		FROM subscriptions WHERE id = ?`, idv)
	sub, err := scanSubscription(row)
	if err != nil {
		return Subscription{}, err
	}
	found := false
	for _, sku := range sub.SKUIDs {
		if sku == skuID {
			found = true
		}
	}
	if !found {
		return Subscription{}, ErrNotFound
	}
	return sub, nil
}

func scanEntitlement(row rowScanner) (Entitlement, error) {
	var (
		e        Entitlement
		user     sql.NullString
		guild    sql.NullString
		consumed int
		starts   sql.NullInt64
		ends     sql.NullInt64
		sub      sql.NullString
	)
	if err := row.Scan(&e.ID, &e.SKUID, &e.ApplicationID, &user, &guild, &e.Type,
		&e.Deleted, &consumed, &starts, &ends, &sub); err != nil {
		return e, mapErr(err)
	}
	e.UserID = ns(user)
	e.GuildID = ns(guild)
	c := consumed != 0
	e.Consumed = &c
	e.StartsAt = isoPtr(starts)
	e.EndsAt = isoPtr(ends)
	e.SubscriptionID = ns(sub)
	return e, nil
}

func scanSubscription(row rowScanner) (Subscription, error) {
	var (
		sub      Subscription
		skus     string
		ents     string
		renewals sql.NullString
		starts   sql.NullInt64
		ends     sql.NullInt64
		canceled sql.NullInt64
	)
	if err := row.Scan(&sub.ID, &sub.UserID, &skus, &ents, &renewals, &starts, &ends,
		&sub.Status, &canceled); err != nil {
		return sub, mapErr(err)
	}
	sub.SKUIDs = parseJSONStrings(skus)
	sub.EntitlementIDs = parseJSONStrings(ents)
	sub.RenewalSKUIDs = []string{}
	if renewals.Valid {
		sub.RenewalSKUIDs = parseJSONStrings(renewals.String)
	}
	sub.CurrentPeriodStart = isoPtr(starts)
	sub.CurrentPeriodEnd = isoPtr(ends)
	sub.CanceledAt = isoPtr(canceled)
	return sub, nil
}

// ValidateEntitlementOwner checks that a user or guild owner id is a snowflake.
func ValidateEntitlementOwner(ownerID string) bool {
	return id.Valid(ownerID)
}
