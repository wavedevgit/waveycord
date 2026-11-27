package api

import (
	"errors"
	"strings"

	"github.com/wavedevgit/waveycord/internal/store"
)

// handleListApplicationSKUs lists an application's premium offerings.
func (s *Server) handleListApplicationSKUs(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	skus, err := s.Store.ListSKUs(appID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(skus)
}

// handleListEntitlements lists entitlements with the documented filters.
func (s *Server) handleListEntitlements(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	q := c.R.URL.Query()
	filter := store.EntitlementFilter{
		UserID:         q.Get("user_id"),
		GuildID:        q.Get("guild_id"),
		Before:         q.Get("before"),
		After:          q.Get("after"),
		ExcludeEnded:   boolQuery(q.Get("exclude_ended")),
		ExcludeDeleted: boolQueryDefault(q.Get("exclude_deleted"), true),
	}
	if raw := q.Get("sku_ids"); raw != "" {
		for _, sku := range strings.Split(raw, ",") {
			if sku = strings.TrimSpace(sku); sku != "" {
				filter.SKUIDs = append(filter.SKUIDs, sku)
			}
		}
	}
	if raw := q.Get("limit"); raw != "" {
		v := 0
		for _, r := range raw {
			if r < '0' || r > '9' {
				v = -1
				break
			}
			v = v*10 + int(r-'0')
		}
		if v < 1 || v > 100 {
			c.FailForm("limit", "Must be between 1 and 100")
			return
		}
		filter.Limit = v
	}
	entitlements, err := s.Store.ListEntitlements(appID, filter)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(entitlements)
}

// handleGetEntitlement returns one entitlement.
func (s *Server) handleGetEntitlement(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	e, err := s.Store.Entitlement(appID, c.Param("entitlementID"))
	if err != nil {
		failEntitlement(c, err)
		return
	}
	c.JSON(e)
}

// handleCreateTestEntitlement grants a SKU to a user or guild.
func (s *Server) handleCreateTestEntitlement(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	var req struct {
		SKUID     string `json:"sku_id"`
		OwnerID   string `json:"owner_id"`
		OwnerType int    `json:"owner_type"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.SKUID == "" {
		c.FailForm("sku_id", "This field is required")
		return
	}
	if !store.ValidateEntitlementOwner(req.OwnerID) {
		c.FailForm("owner_id", "Must be a valid snowflake")
		return
	}
	if req.OwnerType != 1 && req.OwnerType != 2 {
		c.FailForm("owner_type", "Must be 1 (guild) or 2 (user)")
		return
	}
	e, err := s.Store.CreateTestEntitlement(appID, req.SKUID, req.OwnerID, req.OwnerType)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.FailForm("sku_id", "Unknown SKU")
			return
		}
		fail(c, err)
		return
	}
	c.JSON(e)
}

// handleConsumeEntitlement marks a consumable entitlement used.
func (s *Server) handleConsumeEntitlement(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	if err := s.Store.ConsumeEntitlement(appID, c.Param("entitlementID")); err != nil {
		failEntitlement(c, err)
		return
	}
	c.NoContent()
}

// handleDeleteEntitlement soft-deletes an entitlement.
func (s *Server) handleDeleteEntitlement(c *Ctx) {
	appID, ok := s.applicationBotAccess(c)
	if !ok {
		return
	}
	if err := s.Store.DeleteEntitlement(appID, c.Param("entitlementID")); err != nil {
		failEntitlement(c, err)
		return
	}
	c.NoContent()
}

// handleListSKUSubscriptions lists subscriptions containing a SKU.
func (s *Server) handleListSKUSubscriptions(c *Ctx) {
	if _, ok := s.applicationBotAccessForSKU(c); !ok {
		return
	}
	q := c.R.URL.Query()
	userID := q.Get("user_id")
	if userID == "" {
		c.FailForm("user_id", "This field is required")
		return
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		v := 0
		for _, r := range raw {
			if r < '0' || r > '9' {
				v = -1
				break
			}
			v = v*10 + int(r-'0')
		}
		if v < 1 || v > 100 {
			c.FailForm("limit", "Must be between 1 and 100")
			return
		}
		limit = v
	}
	subs, err := s.Store.ListSubscriptions(c.Param("skuID"), userID, q.Get("before"), q.Get("after"), limit)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(subs)
}

// handleGetSKUSubscription returns one subscription scoped to a SKU.
func (s *Server) handleGetSKUSubscription(c *Ctx) {
	if _, ok := s.applicationBotAccessForSKU(c); !ok {
		return
	}
	sub, err := s.Store.Subscription(c.Param("skuID"), c.Param("subscriptionID"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, NewError(404, 0, "Unknown Subscription"))
			return
		}
		fail(c, err)
		return
	}
	c.JSON(sub)
}

// applicationBotAccessForSKU resolves the requesting bot and checks that the
// SKU belongs to its application.
func (s *Server) applicationBotAccessForSKU(c *Ctx) (string, bool) {
	appID, ok := s.applicationBot(c)
	if !ok {
		return "", false
	}
	skus, err := s.Store.ListSKUs(appID)
	if err != nil {
		fail(c, err)
		return "", false
	}
	for _, sku := range skus {
		if sku.ID == c.Param("skuID") {
			return appID, true
		}
	}
	fail(c, NewError(404, 10027, "Unknown SKU"))
	return "", false
}

func failEntitlement(c *Ctx, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(c, NewError(404, 10029, "Unknown entitlement"))
		return
	}
	fail(c, err)
}

func boolQuery(raw string) bool {
	return raw == "true" || raw == "1"
}

func boolQueryDefault(raw string, def bool) bool {
	if raw == "" {
		return def
	}
	return raw == "true" || raw == "1"
}
