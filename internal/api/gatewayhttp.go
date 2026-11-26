package api

import (
	"net/http"
	"time"

	"github.com/wavedevgit/waveycord/internal/gateway"
	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/store"
)

func nowMillisFn() int64 { return time.Now().UnixMilli() }

func (s *Server) handleGateway(c *Ctx) { c.JSON(map[string]any{"url": s.cfg.GatewayURL}) }

func (s *Server) handleGatewayBot(c *Ctx) {
	c.JSON(map[string]any{
		"url":    s.cfg.GatewayURL,
		"shards": 1,
		"session_start_limit": map[string]any{
			"total": 1000, "remaining": 1000, "reset_after": 0, "max_concurrency": 1,
		},
	})
}

func (s *Server) gatewayHandler() http.Handler {
	if s.Hub == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"code":0,"message":"gateway unavailable"}`, http.StatusServiceUnavailable)
		})
	}
	return s.Hub.Handler()
}

// ReadyBuilder builds the documented READY payload for a session.
func ReadyBuilder(st *store.Store, gatewayURL string) gateway.ReadyFunc {
	return func(userID, sessionID string) (any, error) {
		user, err := st.PrivateUser(userID)
		if err != nil {
			return nil, err
		}
		guilds, err := st.GuildsForUser(userID)
		if err != nil {
			return nil, err
		}
		type readyGuild struct {
			model.Guild
			Unavailable   bool                 `json:"unavailable"`
			Channels      []model.Channel      `json:"channels"`
			Members       []model.Member       `json:"members"`
			Threads       []model.Channel      `json:"threads"`
			VoiceStates   []store.VoiceState   `json:"voice_states"`
			Onboarding    *store.Onboarding    `json:"onboarding,omitempty"`
			WelcomeScreen *store.WelcomeScreen `json:"welcome_screen,omitempty"`
			Presences     []any                `json:"presences"`
			MemberCount   int                  `json:"member_count"`
		}
		out := make([]readyGuild, 0, len(guilds))
		for _, g := range guilds {
			channels, _ := st.GuildChannels(g.ID)
			members, _ := st.Members(g.ID, 100, "")
			threads, _ := st.ActiveThreads(g.ID)
			voice, _ := st.VoiceStatesForGuild(g.ID)
			onboarding, _ := st.GetOnboarding(g.ID)
			welcome, _ := st.GetWelcomeScreen(g.ID)
			out = append(out, readyGuild{
				Guild: g, Channels: channels, Members: members, Threads: threads,
				VoiceStates: voice, Presences: []any{}, MemberCount: len(members),
				Onboarding: &onboarding, WelcomeScreen: &welcome,
			})
		}
		dms, _ := st.DMChannels(userID)
		return map[string]any{
			"v":                       10,
			"user":                    user,
			"guilds":                  out,
			"private_channels":        dms,
			"session_id":              sessionID,
			"resume_gateway_url":      gatewayURL,
			"session_type":            "normal",
			"application":             map[string]any{"id": "", "flags": 0},
			"relationships":           []any{},
			"presences":               []any{},
			"user_settings":           map[string]any{},
			"geo_ordered_rtc_regions": []string{"us-east"},
		}, nil
	}
}

// GuildIDsFor is the gateway subscription resolver.
func GuildIDsFor(st *store.Store) gateway.GuildsFunc {
	return func(userID string) []string {
		guilds, err := st.GuildsForUser(userID)
		if err != nil {
			return nil
		}
		out := make([]string, 0, len(guilds))
		for _, g := range guilds {
			out = append(out, g.ID)
		}
		return out
	}
}
