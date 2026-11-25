package api

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/wavedevgit/waveycord/internal/store"
)

var commandNameRe = regexp.MustCompile(`^[-_\p{L}\p{N}]{1,32}$`)

// authorizeApplication accepts the owner (user token) or the application's bot.
func (s *Server) authorizeApplication(c *Ctx, appID string) (store.Application, bool) {
	app, err := s.Store.Application(appID)
	if err != nil {
		c.JSONStatus(404, map[string]any{"code": 10002, "message": "Unknown Application"})
		return app, false
	}
	if app.OwnerID == c.User.ID {
		return app, true
	}
	if app.Bot != nil && app.Bot.ID == c.User.ID {
		return app, true
	}
	fail(c, ErrMissingPerms)
	return app, false
}

func validateCommand(in *store.CommandInput) string {
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if !commandNameRe.MatchString(in.Name) {
		return "name must be 1-32 characters of lowercase letters, numbers, '-' or '_'"
	}
	if len([]rune(in.Description)) > 100 {
		return "description must be 100 characters or fewer"
	}
	if in.Type == 0 {
		in.Type = 1
	}
	if in.Type < 1 || in.Type > 3 {
		return "type must be 1, 2 or 3"
	}
	if in.Type == 1 && strings.TrimSpace(in.Description) == "" {
		return "description is required for CHAT_INPUT commands"
	}
	return ""
}

func (s *Server) handleListCommands(c *Ctx) {
	app, ok := s.authorizeApplication(c, c.Param("applicationID"))
	if !ok {
		return
	}
	commands, err := s.Store.ListCommands(app.ID, c.Param("guildID"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(commands)
}

func (s *Server) handlePutCommands(c *Ctx) {
	app, ok := s.authorizeApplication(c, c.Param("applicationID"))
	if !ok {
		return
	}
	guildID := c.Param("guildID")
	if guildID != "" {
		if _, err := s.requireGuild(c.User.ID, guildID, 0); err != nil && app.OwnerID != c.User.ID {
			fail(c, err)
			return
		}
		if _, err := s.Store.Guild(guildID); err != nil {
			fail(c, ErrUnknownGuild)
			return
		}
	}
	var in []store.CommandInput
	if err := c.Bind(&in); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if len(in) > 100 {
		c.FailForm("commands", "Must be 100 or fewer in length")
		return
	}
	seen := map[string]bool{}
	for i := range in {
		if msg := validateCommand(&in[i]); msg != "" {
			c.FailForm("commands", msg)
			return
		}
		if seen[in[i].Name] {
			c.FailForm("commands", "Command names must be unique")
			return
		}
		seen[in[i].Name] = true
	}
	commands, err := s.Store.ReplaceCommands(app.ID, guildID, in)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(commands)
}

func (s *Server) handleCreateCommand(c *Ctx) {
	app, ok := s.authorizeApplication(c, c.Param("applicationID"))
	if !ok {
		return
	}
	var in store.CommandInput
	if err := c.Bind(&in); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if msg := validateCommand(&in); msg != "" {
		c.FailForm("name", msg)
		return
	}
	guildID := c.Param("guildID")
	if guildID != "" {
		if _, err := s.Store.Guild(guildID); err != nil {
			fail(c, ErrUnknownGuild)
			return
		}
	}
	cmd, err := s.Store.CreateCommand(app.ID, guildID, in)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			c.JSONStatus(400, map[string]any{"code": 50035, "message": "Invalid Form Body",
				"errors": map[string]any{"name": map[string]any{"_errors": []map[string]any{{
					"code": "APPLICATION_COMMAND_NAME_ALREADY_EXISTS", "message": "Command name is already used",
				}}}}})
			return
		}
		fail(c, err)
		return
	}
	c.JSONStatus(201, cmd)
}

func (s *Server) handleGetCommand(c *Ctx) {
	app, ok := s.authorizeApplication(c, c.Param("applicationID"))
	if !ok {
		return
	}
	cmd, err := s.Store.Command(c.Param("commandID"))
	if err != nil || cmd.ApplicationID != app.ID {
		fail(c, NewError(404, 10063, "Unknown application command"))
		return
	}
	c.JSON(cmd)
}

func (s *Server) handleUpdateCommand(c *Ctx) {
	app, ok := s.authorizeApplication(c, c.Param("applicationID"))
	if !ok {
		return
	}
	cmd, err := s.Store.Command(c.Param("commandID"))
	if err != nil || cmd.ApplicationID != app.ID {
		fail(c, NewError(404, 10063, "Unknown application command"))
		return
	}
	var in store.CommandInput
	if err := c.Bind(&in); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if in.Name != "" {
		if msg := validateCommand(&in); msg != "" {
			c.FailForm("name", msg)
			return
		}
	}
	updated, err := s.Store.UpdateCommand(cmd.ID, in)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(updated)
}

func (s *Server) handleDeleteCommand(c *Ctx) {
	app, ok := s.authorizeApplication(c, c.Param("applicationID"))
	if !ok {
		return
	}
	cmd, err := s.Store.Command(c.Param("commandID"))
	if err != nil || cmd.ApplicationID != app.ID {
		fail(c, NewError(404, 10063, "Unknown application command"))
		return
	}
	if err := s.Store.DeleteCommand(cmd.ID); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

// commandResponse is the documented interaction callback for a command.
type commandResponse struct {
	Type int            `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

func interactionMessage(content string) commandResponse {
	return commandResponse{Type: 4, Data: map[string]any{
		"content":          content,
		"allowed_mentions": map[string]any{"parse": []string{}},
	}}
}

// dispatchCommand executes a registered command. Discord forwards invocations to
// the application's own endpoint; a self-hosted deployment has no external bot to
// call, so the dispatch target is in-process. The lookup, scope resolution and
// callback envelope are the documented ones.
func dispatchCommand(cmd store.ApplicationCommand, options json.RawMessage) commandResponse {
	switch cmd.Name {
	case "hello":
		return interactionMessage("Hello from " + cmd.Name + "!")
	case "echo":
		var opts []struct {
			Name  string `json:"name"`
			Value any    `json:"value"`
		}
		if json.Unmarshal(options, &opts) == nil {
			for _, o := range opts {
				if o.Name == "text" {
					if s, ok := o.Value.(string); ok {
						return interactionMessage(s)
					}
				}
			}
		}
		return interactionMessage("Echo needs a text option.")
	default:
		return interactionMessage("Command `" + cmd.Name + "` executed.")
	}
}
