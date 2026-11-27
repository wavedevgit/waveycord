package api

import (
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wavedevgit/waveycord/internal/id"
	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
)

// maxTargetUsers is the documented bulk ceiling.
const maxTargetUsers = 1000

// inviteForTargets resolves an invite and checks the documented caller rules:
// inviter, MANAGE_GUILD, or for reads VIEW_AUDIT_LOG.
func (s *Server) inviteForTargets(c *Ctx, readOnly bool) (model.Invite, bool) {
	inv, err := s.Store.Invite(c.Param("code"))
	if err != nil {
		fail(c, ErrUnknownInvite)
		return model.Invite{}, false
	}
	if inv.Inviter != nil && inv.Inviter.ID == c.User.ID {
		return inv, true
	}
	if inv.Guild != nil {
		perms, err := s.guildPermissions(c.User.ID, inv.Guild.ID)
		if err != nil {
			fail(c, err)
			return model.Invite{}, false
		}
		if perms&perm.ManageGuild != 0 || perms&perm.Administrator != 0 {
			return inv, true
		}
		if readOnly && perms&perm.ViewAuditLog != 0 {
			return inv, true
		}
	}
	fail(c, NewError(403, 50013, "Missing Permissions"))
	return model.Invite{}, false
}

// validTargetUserIDs validates snowflakes and dedupes them.
func validTargetUserIDs(ids []string) ([]string, []string) {
	out := []string{}
	bad := []string{}
	seen := map[string]bool{}
	for _, idv := range ids {
		idv = strings.TrimSpace(idv)
		if idv == "" || seen[idv] {
			continue
		}
		seen[idv] = true
		if !id.Valid(idv) {
			bad = append(bad, idv)
			continue
		}
		out = append(out, idv)
	}
	return out, bad
}

// handleGetInviteTargetUsers returns the CSV documented response.
func (s *Server) handleGetInviteTargetUsers(c *Ctx) {
	inv, ok := s.inviteForTargets(c, true)
	if !ok {
		return
	}
	users, err := s.Store.InviteTargetUsers(inv.Code)
	if err != nil {
		fail(c, err)
		return
	}
	c.W.Header().Set("Content-Type", "text/csv")
	c.W.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(c.W, "user_id\n")
	for _, userID := range users {
		_, _ = io.WriteString(c.W, userID+"\n")
	}
}

// handleAddInviteTargetUser adds one user to an invite.
func (s *Server) handleAddInviteTargetUser(c *Ctx) {
	inv, ok := s.inviteForTargets(c, false)
	if !ok {
		return
	}
	userID := c.Param("userID")
	if _, bad := validTargetUserIDs([]string{userID}); len(bad) > 0 {
		c.FailForm("user_id", "Must be a valid snowflake")
		return
	}
	if err := s.Store.AddInviteTargetUsers(inv.Code, []string{userID}); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

// handleRemoveInviteTargetUser removes one user from an invite.
func (s *Server) handleRemoveInviteTargetUser(c *Ctx) {
	inv, ok := s.inviteForTargets(c, false)
	if !ok {
		return
	}
	if err := s.Store.RemoveInviteTargetUsers(inv.Code, []string{c.Param("userID")}); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

// handleBulkAddInviteTargetUsers adds up to 1000 users.
func (s *Server) handleBulkAddInviteTargetUsers(c *Ctx) {
	s.bulkInviteTargets(c, true)
}

// handleBulkDeleteInviteTargetUsers removes up to 1000 users.
func (s *Server) handleBulkDeleteInviteTargetUsers(c *Ctx) {
	s.bulkInviteTargets(c, false)
}

func (s *Server) bulkInviteTargets(c *Ctx, add bool) {
	inv, ok := s.inviteForTargets(c, false)
	if !ok {
		return
	}
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if len(req.UserIDs) == 0 || len(req.UserIDs) > maxTargetUsers {
		c.FailForm("user_ids", "Must contain between 1 and 1000 ids")
		return
	}
	users, bad := validTargetUserIDs(req.UserIDs)
	if len(bad) > 0 {
		c.FailForm("user_ids", "Contains invalid user ids")
		return
	}
	var err error
	if add {
		err = s.Store.AddInviteTargetUsers(inv.Code, users)
	} else {
		err = s.Store.RemoveInviteTargetUsers(inv.Code, users)
	}
	if err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

// handlePutInviteTargetUsers replaces the list from a CSV upload, or from a
// JSON user_ids array when sent as JSON.
func (s *Server) handlePutInviteTargetUsers(c *Ctx) {
	inv, ok := s.inviteForTargets(c, false)
	if !ok {
		return
	}
	var users []string
	var bad []string
	if strings.HasPrefix(c.R.Header.Get("Content-Type"), "application/json") {
		var req struct {
			UserIDs []string `json:"user_ids"`
		}
		if err := c.Bind(&req); err != nil {
			fail(c, ErrInvalidForm)
			return
		}
		if len(req.UserIDs) > maxTargetUsers {
			c.FailForm("user_ids", "Must contain 1000 or fewer ids")
			return
		}
		users, bad = validTargetUserIDs(req.UserIDs)
	} else {
		if err := c.R.ParseMultipartForm(maxTargetUsers * 64); err != nil {
			c.FailForm("target_users_file", "Must be a CSV file")
			return
		}
		file, _, err := c.R.FormFile("target_users_file")
		if err != nil {
			c.FailForm("target_users_file", "This field is required")
			return
		}
		defer file.Close()
		records, err := csv.NewReader(file).ReadAll()
		if err != nil {
			c.FailForm("target_users_file", "Must be a valid CSV file")
			return
		}
		raw := []string{}
		for i, rec := range records {
			if len(rec) == 0 {
				continue
			}
			value := strings.TrimSpace(rec[0])
			if i == 0 && strings.EqualFold(value, "user_id") {
				continue
			}
			raw = append(raw, value)
		}
		if len(raw) > maxTargetUsers {
			c.FailForm("target_users_file", "Must contain 1000 or fewer ids")
			return
		}
		users, bad = validTargetUserIDs(raw)
	}
	if len(bad) > 0 {
		c.FailForm("target_users_file", "invalid user ID - must be a valid Discord snowflake")
		return
	}
	if err := s.Store.ReplaceInviteTargetUsers(inv.Code, users); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

// handleInviteTargetUsersJobStatus reports a completed CSV import. Imports are
// synchronous, so every job is already finished when queried.
func (s *Server) handleInviteTargetUsersJobStatus(c *Ctx) {
	inv, ok := s.inviteForTargets(c, true)
	if !ok {
		return
	}
	users, err := s.Store.InviteTargetUsers(inv.Code)
	if err != nil {
		fail(c, err)
		return
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")
	c.JSON(map[string]any{
		"status": 3, "total_users": len(users), "processed_users": len(users),
		"created_at": now, "completed_at": now,
	})
}
