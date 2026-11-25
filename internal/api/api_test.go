package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"context"
	"github.com/wavedevgit/waveycord/internal/api"
	"github.com/wavedevgit/waveycord/internal/db"

	"github.com/wavedevgit/waveycord/internal/gateway"
	"github.com/wavedevgit/waveycord/internal/search"
	"github.com/wavedevgit/waveycord/internal/sqlx"
	"github.com/wavedevgit/waveycord/internal/store"
)

type harness struct {
	t      *testing.T
	server *httptest.Server
	store  *store.Store
	hub    *gateway.Hub
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	var dsn string
	if pg := os.Getenv("WAVEYCORD_TEST_DSN"); pg != "" {
		dsn = pg
	} else {
		dsn = filepath.Join(t.TempDir(), "test.db")
	}
	handle, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if handle.Dialect.String() == "postgres" {
		truncateAll(t, handle)
	}
	t.Cleanup(func() { handle.Close() })
	st := store.New(handle)
	ix, err := search.Open(context.Background(), handle)
	if err != nil {
		t.Fatalf("open search index: %v", err)
	}
	st.SetSearchIndex(ix)
	if replicaDSN := os.Getenv("WAVEYCORD_TEST_REPLICA_DSN"); replicaDSN != "" {
		replica, err := db.OpenReplica(replicaDSN)
		if err != nil {
			t.Fatalf("open replica: %v", err)
		}
		t.Cleanup(func() { replica.Close() })
		st.SetReadReplica(replica)
	}
	cfg := api.DefaultConfig()
	cfg.RateLimit = 10000
	cfg.UploadDir = t.TempDir()
	hub := gateway.NewHub(
		func(token string) (string, bool) {
			_, user, err := st.SessionByToken(api.TokenHash(token))
			return user.ID, err == nil
		},
		api.ReadyBuilder(st, cfg.GatewayURL),
		api.GuildIDsFor(st),
		nil,
	)
	srv := httptest.NewServer(api.New(st, hub, cfg).Handler())
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})
	return &harness{t: t, server: srv, store: st, hub: hub}
}

// truncateAll clears every table so one Postgres database can serve sequential
// tests. SQLite runs each test on a fresh file instead.
func truncateAll(t *testing.T, handle *sqlx.DB) {
	t.Helper()
	rows, err := handle.Query(`SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan table: %v", err)
		}
		names = append(names, name)
	}
	rows.Close()
	for _, name := range names {
		if _, err := handle.Exec(`TRUNCATE TABLE "` + name + `" CASCADE`); err != nil {
			t.Fatalf("truncate %s: %v", name, err)
		}
	}
}

func (h *harness) request(method, path, token string, body any) (int, []byte) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v10"+path, reader)
	if err != nil {
		h.t.Fatalf("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// requestWithToken sends a request with an explicit Authorization value, needed
// for bearer tokens that are not harness accounts.
func (h *harness) requestWithToken(method, path, auth string, body any) (int, []byte) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v10"+path, reader)
	if err != nil {
		h.t.Fatalf("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (h *harness) decode(method, path, token string, body any, out any) int {
	h.t.Helper()
	status, raw := h.request(method, path, token, body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("%s %s: decode %q: %v", method, path, string(raw), err)
		}
	}
	return status
}

type account struct {
	Token string
	ID    string
}

func (h *harness) register(username string) account {
	h.t.Helper()
	var out struct{ Token string }
	status := h.decode("POST", "/auth/register", "", map[string]any{
		"username": username,
		"password": "correct horse battery",
		"email":    username + "@example.test",
	}, &out)
	if status != 201 || out.Token == "" {
		h.t.Fatalf("register %s: status %d", username, status)
	}
	var me struct{ ID, Username string }
	if status := h.decode("GET", "/users/@me", out.Token, nil, &me); status != 200 {
		h.t.Fatalf("get self: %d", status)
	}
	return account{Token: out.Token, ID: me.ID}
}

func TestAuthRegisterLoginLogout(t *testing.T) {
	h := newHarness(t)
	acc := h.register("alice")

	var me struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	if status := h.decode("GET", "/users/@me", acc.Token, nil, &me); status != 200 {
		t.Fatalf("status %d", status)
	}
	if me.ID != acc.ID || me.Username != "alice" || me.Email != "alice@example.test" {
		t.Fatalf("unexpected self %+v", me)
	}

	if status, _ := h.request("GET", "/users/@me", "", nil); status != 401 {
		t.Fatalf("unauthenticated status = %d", status)
	}
	if status, _ := h.request("GET", "/users/@me", "not-a-token", nil); status != 401 {
		t.Fatalf("bad token status = %d", status)
	}

	var login struct{ Token string }
	if status := h.decode("POST", "/auth/login", "", map[string]any{
		"login": "alice@example.test", "password": "correct horse battery",
	}, &login); status != 200 || login.Token == "" {
		t.Fatalf("login status %d token %q", status, login.Token)
	}
	if status, _ := h.request("POST", "/auth/login", "", map[string]any{
		"login": "alice@example.test", "password": "wrong password",
	}); status != 400 {
		t.Fatalf("bad login status = %d", status)
	}

	var sessions []struct{ ID string }
	if status := h.decode("GET", "/auth/sessions", acc.Token, nil, &sessions); status != 200 || len(sessions) != 2 {
		t.Fatalf("sessions status %d len %d", status, len(sessions))
	}
	if status, _ := h.request("POST", "/auth/logout", acc.Token, nil); status != 204 {
		t.Fatalf("logout status = %d", status)
	}
	if status, _ := h.request("GET", "/users/@me", acc.Token, nil); status != 401 {
		t.Fatalf("token valid after logout: %d", status)
	}
}

func TestDuplicateRegistrationRejected(t *testing.T) {
	h := newHarness(t)
	h.register("bob")
	status, raw := h.request("POST", "/auth/register", "", map[string]any{
		"username": "bob2", "password": "correct horse battery", "email": "bob@example.test",
	})
	if status != 400 || !bytes.Contains(raw, []byte("50035")) {
		t.Fatalf("status %d body %s", status, raw)
	}
}

func TestGuildChannelMessageFlow(t *testing.T) {
	h := newHarness(t)
	alice := h.register("gina")

	var guild struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Owner bool   `json:"owner"`
		Roles []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Permissions string `json:"permissions"`
		} `json:"roles"`
	}
	if status := h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Test Guild"}, &guild); status != 201 {
		t.Fatalf("create guild status %d", status)
	}
	if guild.ID == "" || guild.Name != "Test Guild" || len(guild.Roles) != 1 || guild.Roles[0].Name != "@everyone" {
		t.Fatalf("unexpected guild %+v", guild)
	}

	var channel struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Type     int    `json:"type"`
		Position *int   `json:"position"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{
		"name": "general", "type": 0, "topic": "hello",
	}, &channel); status != 201 {
		t.Fatalf("create channel status %d", status)
	}
	if channel.Name != "general" || channel.Type != 0 {
		t.Fatalf("unexpected channel %+v", channel)
	}

	var channels []struct{ ID string }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/channels", alice.Token, nil, &channels); status != 200 || len(channels) != 1 {
		t.Fatalf("channels status %d len %d", status, len(channels))
	}

	var msg struct {
		ID        string `json:"id"`
		ChannelID string `json:"channel_id"`
		Content   string `json:"content"`
		Author    struct{ ID string }
		Timestamp string `json:"timestamp"`
	}
	if status := h.decode("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{
		"content": "first message",
	}, &msg); status != 200 {
		t.Fatalf("create message status %d", status)
	}
	if msg.Content != "first message" || msg.Author.ID != alice.ID || msg.ChannelID != channel.ID {
		t.Fatalf("unexpected message %+v", msg)
	}

	if status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{"content": "   "}); status != 400 {
		t.Fatalf("empty message status = %d", status)
	}

	var fetched struct{ ID, Content string }
	if status := h.decode("GET", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token, nil, &fetched); status != 200 || fetched.Content != "first message" {
		t.Fatalf("fetch message status %d content %q", status, fetched.Content)
	}

	var edited struct {
		Content         string  `json:"content"`
		EditedTimestamp *string `json:"edited_timestamp"`
	}
	if status := h.decode("PATCH", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token, map[string]any{
		"content": "edited",
	}, &edited); status != 200 || edited.Content != "edited" || edited.EditedTimestamp == nil {
		t.Fatalf("edit message status %d %+v", status, edited)
	}

	// Second message, then pagination: before/after/limit.
	for i := 0; i < 3; i++ {
		if status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token,
			map[string]any{"content": fmt.Sprintf("msg %d", i)}); status != 200 {
			t.Fatalf("seed message %d failed", i)
		}
	}
	var page []struct{ ID string }
	if status := h.decode("GET", "/channels/"+channel.ID+"/messages?limit=2", alice.Token, nil, &page); status != 200 || len(page) != 2 {
		t.Fatalf("page status %d len %d", status, len(page))
	}
	if page[0].ID < page[1].ID {
		t.Fatalf("messages not newest-first: %v", page)
	}
	var before []struct{ ID string }
	if status := h.decode("GET", "/channels/"+channel.ID+"/messages?before="+page[0].ID+"&limit=10", alice.Token, nil, &before); status != 200 {
		t.Fatalf("before status %d", status)
	}
	for _, m := range before {
		if m.ID >= page[0].ID {
			t.Fatalf("before returned %s >= %s", m.ID, page[0].ID)
		}
	}

	// Reactions.
	if status, _ := h.request("PUT", "/channels/"+channel.ID+"/messages/"+msg.ID+"/reactions/%F0%9F%91%8D/@me", alice.Token, nil); status != 204 {
		t.Fatalf("add reaction status = %d", status)
	}
	var withReaction struct {
		Reactions []struct {
			Count int `json:"count"`
			Emoji struct{ Name string }
		}
	}
	if status := h.decode("GET", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token, nil, &withReaction); status != 200 ||
		len(withReaction.Reactions) != 1 || withReaction.Reactions[0].Count != 1 {
		t.Fatalf("reactions %+v", withReaction.Reactions)
	}
	var reactors []struct{ ID string }
	if status := h.decode("GET", "/channels/"+channel.ID+"/messages/"+msg.ID+"/reactions/%F0%9F%91%8D", alice.Token, nil, &reactors); status != 200 || len(reactors) != 1 || reactors[0].ID != alice.ID {
		t.Fatalf("reaction users %+v", reactors)
	}

	// Pins.
	if status, _ := h.request("PUT", "/channels/"+channel.ID+"/pins/"+msg.ID, alice.Token, nil); status != 204 {
		t.Fatalf("pin status = %d", status)
	}
	var pins []struct{ ID string }
	if status := h.decode("GET", "/channels/"+channel.ID+"/pins", alice.Token, nil, &pins); status != 200 || len(pins) != 1 || pins[0].ID != msg.ID {
		t.Fatalf("pins %+v", pins)
	}

	if status, _ := h.request("DELETE", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token, nil); status != 204 {
		t.Fatalf("delete message status = %d", status)
	}
	if status, _ := h.request("GET", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token, nil); status != 404 {
		t.Fatalf("deleted message status = %d", status)
	}
}

func TestInviteJoinAndPermissions(t *testing.T) {
	h := newHarness(t)
	alice := h.register("owner")
	bob := h.register("guest")

	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Perm Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "locked"}, &channel)

	// Bob is not a member yet.
	if status, _ := h.request("GET", "/guilds/"+guild.ID, bob.Token, nil); status != 403 {
		t.Fatalf("non-member guild read = %d", status)
	}

	var invite struct{ Code string }
	if status := h.decode("POST", "/channels/"+channel.ID+"/invites", alice.Token, map[string]any{"max_age": 3600}, &invite); status != 201 || invite.Code == "" {
		t.Fatalf("create invite status %d", status)
	}
	var public struct {
		Code  string
		Guild *struct{ ID string }
	}
	if status := h.decode("GET", "/invites/"+invite.Code, "", nil, &public); status != 200 || public.Guild == nil || public.Guild.ID != guild.ID {
		t.Fatalf("public invite status %d %+v", status, public)
	}
	if status := h.decode("POST", "/invites/"+invite.Code, bob.Token, nil, nil); status != 200 {
		t.Fatalf("accept invite status %d", status)
	}

	var member struct {
		User  struct{ ID string }
		Roles []string
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/members/"+bob.ID, alice.Token, nil, &member); status != 200 || member.User.ID != bob.ID {
		t.Fatalf("member status %d %+v", status, member)
	}

	// Default @everyone grants SEND_MESSAGES, so Bob can post.
	if status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", bob.Token, map[string]any{"content": "hi"}); status != 200 {
		t.Fatalf("member send status = %d", status)
	}

	// Deny SEND_MESSAGES for @everyone on the channel.
	if status, _ := h.request("PUT", "/channels/"+channel.ID+"/permissions/"+guild.ID, alice.Token, map[string]any{
		"type": 0, "allow": "0", "deny": "2048",
	}); status != 204 {
		t.Fatalf("put overwrite status = %d", status)
	}
	if status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", bob.Token, map[string]any{"content": "blocked"}); status != 403 {
		t.Fatalf("denied send status = %d", status)
	}
	// Reading history is still allowed.
	if status, _ := h.request("GET", "/channels/"+channel.ID+"/messages", bob.Token, nil); status != 200 {
		t.Fatalf("read after deny status = %d", status)
	}
	// An explicit member allow overrides the role deny.
	if status, _ := h.request("PUT", "/channels/"+channel.ID+"/permissions/"+bob.ID, alice.Token, map[string]any{
		"type": 1, "allow": "2048", "deny": "0",
	}); status != 204 {
		t.Fatalf("member overwrite status = %d", status)
	}
	if status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", bob.Token, map[string]any{"content": "allowed"}); status != 200 {
		t.Fatalf("allowed send status = %d", status)
	}
}

func TestRolesMembersAndBans(t *testing.T) {
	h := newHarness(t)
	alice := h.register("admin")
	bob := h.register("target")

	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Mod Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "mods"}, &channel)

	var role struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Permissions string `json:"permissions"`
	}
	if status := h.decode("POST", "/guilds/"+guild.ID+"/roles", alice.Token, map[string]any{
		"name": "Moderator", "permissions": "8192", "color": 3447003,
	}, &role); status != 201 || role.Name != "Moderator" {
		t.Fatalf("create role status %d %+v", status, role)
	}
	var roles []struct{ ID string }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/roles", alice.Token, nil, &roles); status != 200 || len(roles) != 2 {
		t.Fatalf("roles status %d len %d", status, len(roles))
	}

	var invite struct{ Code string }
	h.decode("POST", "/channels/"+channel.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	if status, _ := h.request("PUT", "/guilds/"+guild.ID+"/members/"+bob.ID+"/roles/"+role.ID, alice.Token, nil); status != 204 {
		t.Fatalf("add role status = %d", status)
	}
	var member struct{ Roles []string }
	h.decode("GET", "/guilds/"+guild.ID+"/members/"+bob.ID, alice.Token, nil, &member)
	if len(member.Roles) != 1 || member.Roles[0] != role.ID {
		t.Fatalf("member roles %+v", member.Roles)
	}
	if status, _ := h.request("DELETE", "/guilds/"+guild.ID+"/members/"+bob.ID+"/roles/"+role.ID, alice.Token, nil); status != 204 {
		t.Fatalf("remove role status = %d", status)
	}

	if status, _ := h.request("PATCH", "/guilds/"+guild.ID+"/members/"+bob.ID, alice.Token, map[string]any{"nick": "Bobby"}); status != 200 {
		t.Fatalf("nick status = %d", status)
	}

	if status, _ := h.request("PUT", "/guilds/"+guild.ID+"/bans/"+bob.ID, alice.Token, map[string]any{}); status != 204 {
		t.Fatalf("ban status = %d", status)
	}
	var bans []struct{ User struct{ ID string } }
	if status := h.decode("GET", "/guilds/"+guild.ID+"/bans", alice.Token, nil, &bans); status != 200 || len(bans) != 1 || bans[0].User.ID != bob.ID {
		t.Fatalf("bans %+v", bans)
	}
	if status, _ := h.request("GET", "/guilds/"+guild.ID+"/members/"+bob.ID, alice.Token, nil); status != 404 {
		t.Fatalf("banned member status = %d", status)
	}
	if status, _ := h.request("DELETE", "/guilds/"+guild.ID+"/bans/"+bob.ID, alice.Token, nil); status != 204 {
		t.Fatalf("unban status = %d", status)
	}
}

func TestDMsAndRecipients(t *testing.T) {
	h := newHarness(t)
	alice := h.register("sender")
	bob := h.register("receiver")

	var dm struct {
		ID         string `json:"id"`
		Type       int    `json:"type"`
		Recipients []struct{ ID string }
	}
	if status := h.decode("POST", "/users/@me/channels", alice.Token, map[string]any{"recipient_id": bob.ID}, &dm); status != 200 || dm.Type != 1 || len(dm.Recipients) != 2 {
		t.Fatalf("dm status %d %+v", status, dm)
	}
	// A second open returns the same channel.
	var again struct{ ID string }
	h.decode("POST", "/users/@me/channels", alice.Token, map[string]any{"recipient_id": bob.ID}, &again)
	if again.ID != dm.ID {
		t.Fatalf("dm not reused: %s != %s", again.ID, dm.ID)
	}
	if status, _ := h.request("POST", "/channels/"+dm.ID+"/messages", bob.Token, map[string]any{"content": "hey"}); status != 200 {
		t.Fatalf("dm send status = %d", status)
	}
	var list []struct{ ID string }
	if status := h.decode("GET", "/users/@me/channels", bob.Token, nil, &list); status != 200 || len(list) != 1 {
		t.Fatalf("dm list status %d len %d", status, len(list))
	}
	// A third account cannot read it.
	carol := h.register("intruder")
	if status, _ := h.request("GET", "/channels/"+dm.ID+"/messages", carol.Token, nil); status != 403 {
		t.Fatalf("dm intruder status = %d", status)
	}
}

func TestThreads(t *testing.T) {
	h := newHarness(t)
	alice := h.register("threader")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Thread Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "talk"}, &channel)

	var thread struct {
		ID             string  `json:"id"`
		Type           int     `json:"type"`
		ParentID       *string `json:"parent_id"`
		ThreadMetadata *struct {
			Archived            bool `json:"archived"`
			AutoArchiveDuration int  `json:"auto_archive_duration"`
		} `json:"thread_metadata"`
	}
	if status := h.decode("POST", "/channels/"+channel.ID+"/threads", alice.Token, map[string]any{
		"name": "topic", "auto_archive_duration": 60, "type": 11,
	}, &thread); status != 201 || thread.Type != 11 || thread.ParentID == nil || *thread.ParentID != channel.ID {
		t.Fatalf("thread status %d %+v", status, thread)
	}
	var members []struct {
		UserID string `json:"user_id"`
	}
	if status := h.decode("GET", "/channels/"+thread.ID+"/thread-members", alice.Token, nil, &members); status != 200 || len(members) != 1 {
		t.Fatalf("thread members status %d %+v", status, members)
	}
	if status, _ := h.request("POST", "/channels/"+thread.ID+"/messages", alice.Token, map[string]any{"content": "in thread"}); status != 200 {
		t.Fatalf("thread message status = %d", status)
	}
	var active struct {
		Threads []struct{ ID string }
	}
	if status := h.decode("GET", "/guilds/"+guild.ID+"/threads/active", alice.Token, nil, &active); status != 200 || len(active.Threads) != 1 {
		t.Fatalf("active threads %d %+v", status, active)
	}
	// Archive it.
	if status, _ := h.request("PATCH", "/channels/"+thread.ID, alice.Token, map[string]any{"archived": true}); status != 200 {
		t.Fatalf("archive status = %d", status)
	}
	var archived struct {
		Threads []struct{ ID string }
	}
	if status := h.decode("GET", "/channels/"+channel.ID+"/threads/archived/public", alice.Token, nil, &archived); status != 200 || len(archived.Threads) != 1 {
		t.Fatalf("archived threads %d %+v", status, archived)
	}
}

func TestGatewayIdentifyAndDispatch(t *testing.T) {
	h := newHarness(t)
	alice := h.register("gatewayuser")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "WS Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "live"}, &channel)

	wsURL := "ws" + h.server.URL[len("http"):] + "/gateway"
	deadline := time.Now().Add(10 * time.Second)
	conn, _, err := dialGateway(wsURL, deadline)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	hello := readFrame(t, conn, deadline)
	if hello["op"].(float64) != 10 {
		t.Fatalf("expected HELLO, got %+v", hello)
	}
	writeFrame(t, conn, map[string]any{"op": 2, "d": map[string]any{
		"token": alice.Token, "intents": 0, "properties": map[string]any{},
	}})
	ready := readFrame(t, conn, deadline)
	if ready["t"] != "READY" {
		t.Fatalf("expected READY, got %+v", ready)
	}
	d := ready["d"].(map[string]any)
	if d["session_id"] == "" {
		t.Fatalf("missing session_id: %+v", d)
	}

	writeFrame(t, conn, map[string]any{"op": 1, "d": nil})
	ack := readFrame(t, conn, deadline)
	if ack["op"].(float64) != 11 {
		t.Fatalf("expected HEARTBEAT_ACK, got %+v", ack)
	}

	// A message posted over REST must arrive on the gateway.
	status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token, map[string]any{"content": "live"})
	if status != 200 {
		t.Fatalf("send status %d", status)
	}
	event := readFrame(t, conn, deadline)
	for i := 0; event["t"] != "MESSAGE_CREATE" && i < 3; i++ {
		event = readFrame(t, conn, deadline)
	}
	if event["t"] != "MESSAGE_CREATE" {
		t.Fatalf("expected MESSAGE_CREATE, got %+v", event)
	}
	md := event["d"].(map[string]any)
	if md["content"] != "live" || md["channel_id"] != channel.ID {
		t.Fatalf("unexpected dispatch %+v", md)
	}
}
