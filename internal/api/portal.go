package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"net/http"
	"strings"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/store"
)

// Developer portal: server-rendered pages over the same store the REST API uses.
// Authentication is a session cookie so a browser can use it; the REST API keeps
// using Authorization headers.

const (
	portalCookie     = "waveycord_portal"
	portalCSRFCookie = "waveycord_portal_csrf"
)

// csrfToken returns the double-submit token, creating it when absent.
func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(portalCSRFCookie); err == nil && len(c.Value) >= 16 {
		return c.Value
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	s.setPortalCookie(w, r, portalCSRFCookie, token)
	return token
}

// setPortalCookie applies the Secure attribute on TLS (or when forced by config).
func (s *Server) setPortalCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	secure := s.cfg.PortalCookieSecure || r.TLS != nil
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/portal",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure})
}

// checkCSRF compares the form token with the cookie in constant time.
func (s *Server) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(portalCSRFCookie)
	if err != nil || len(c.Value) < 16 {
		return false
	}
	form := r.FormValue("csrf_token")
	if form == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(form)) == 1
}

type portalPermRow struct {
	Command string
	Target  string
	Type    int
	Allow   bool
}

type portalPage struct {
	Title    string
	CSRF     string
	User     *model.User
	Apps     []store.Application
	App      *store.Application
	Commands []store.ApplicationCommand
	PermRows []portalPermRow
	GuildID  string
	Endpoint string
	Flash    string
	Error    string
}

var portalTemplates = template.Must(template.New("portal").Parse(`
{{define "head"}}<!doctype html><html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:56rem;margin:2rem auto;padding:0 1rem;background:#1e1f22;color:#dbdee1}
a{color:#5865f2}table{border-collapse:collapse;width:100%}td,th{border-bottom:1px solid #3f4147;padding:.4rem;text-align:left}
input,textarea{background:#2b2d31;color:#dbdee1;border:1px solid #3f4147;padding:.35rem;width:100%}
button{background:#5865f2;color:#fff;border:0;padding:.45rem .9rem;cursor:pointer}
code{background:#2b2d31;padding:.1rem .3rem}.err{color:#f23f43}.ok{color:#3ba55d}</style></head><body>
{{if .User}}<p>Signed in as <strong>{{.User.Username}}</strong> · <a href="/portal">Apps</a> · <a href="/portal/logout">Sign out</a></p>{{end}}
<h1>{{.Title}}</h1>{{if .Flash}}<p class="ok">{{.Flash}}</p>{{end}}{{if .Error}}<p class="err">{{.Error}}</p>{{end}}{{end}}
{{define "foot"}}</body></html>{{end}}

{{define "login"}}{{template "head" .}}
<form method="post" action="/portal/login"><input type="hidden" name="csrf_token" value="{{.CSRF}}">
<p><label>Email or username<br><input name="login" autofocus></label></p>
<p><label>Password<br><input name="password" type="password"></label></p>
<p><button type="submit">Sign in</button></p>
</form>{{template "foot" .}}{{end}}

{{define "dashboard"}}{{template "head" .}}
<p><a href="/portal/applications/new">New application</a></p>
<table><tr><th>Name</th><th>ID</th></tr>
{{range .Apps}}<tr><td><a href="/portal/applications/{{.ID}}">{{.Name}}</a></td><td><code>{{.ID}}</code></td></tr>{{else}}
<tr><td colspan="2">No applications yet.</td></tr>{{end}}
</table>{{template "foot" .}}{{end}}

{{define "newapp"}}{{template "head" .}}
<form method="post" action="/portal/applications"><input type="hidden" name="csrf_token" value="{{.CSRF}}">
<p><label>Name<br><input name="name" maxlength="64"></label></p>
<p><label>Description<br><textarea name="description" rows="3"></textarea></label></p>
<p><button type="submit">Create application</button></p>
</form>{{template "foot" .}}{{end}}

{{define "app"}}{{template "head" .}}
<p>ID <code>{{.App.ID}}</code> · bot <code>{{.App.Bot.ID}}</code> · <a href="/portal/applications/{{.App.ID}}/settings">Settings</a></p>
<form method="post" action="/portal/applications/{{.App.ID}}/token"><input type="hidden" name="csrf_token" value="{{.CSRF}}">
<p><button type="submit">Reset bot token</button></p></form>
<h2>Commands</h2>
<table><tr><th>Name</th><th>Description</th><th>ID</th></tr>
{{range .Commands}}<tr><td>{{.Name}}</td><td>{{.Description}}</td><td><code>{{.ID}}</code></td></tr>{{else}}
<tr><td colspan="3">No commands.</td></tr>{{end}}</table>
<h3>New command</h3>
<form method="post" action="/portal/applications/{{.App.ID}}/commands"><input type="hidden" name="csrf_token" value="{{.CSRF}}">
<p><label>Name<br><input name="name" maxlength="32"></label></p>
<p><label>Description<br><input name="description" maxlength="100"></label></p>
<p><button type="submit">Create command</button></p>
</form>
<h2>Command permissions</h2>
<form method="get" action="/portal/applications/{{.App.ID}}/permissions">
<p><label>Guild ID<br><input name="guild_id" value="{{.GuildID}}"></label></p>
<p><button type="submit">Load</button></p></form>
{{if .PermRows}}
<table><tr><th>Command</th><th>Target</th><th>Type</th><th>Allowed</th></tr>
{{range .PermRows}}<tr><td><code>{{.Command}}</code></td><td>{{.Target}}</td><td>{{.Type}}</td><td>{{.Allow}}</td></tr>{{end}}
</table>{{end}}
<h3>Set permission</h3>
<form method="post" action="/portal/applications/{{.App.ID}}/permissions"><input type="hidden" name="csrf_token" value="{{.CSRF}}">
<p><label>Guild ID<br><input name="guild_id" value="{{.GuildID}}"></label></p>
<p><label>Command ID<br><input name="command_id"></label></p>
<p><label>Target ID<br><input name="target_id"></label></p>
<p><label>Type (2=role, 3=user)<br><input name="type" value="2"></label></p>
<p><label>Allowed (true/false)<br><input name="allow" value="true"></label></p>
<p><button type="submit">Save permission</button></p>
</form>{{template "foot" .}}{{end}}

{{define "secret"}}{{template "head" .}}
<p>Client secret (shown once):</p>
<p><code id="client-secret">{{.Flash}}</code></p>
<p><a href="/portal/applications/{{.App.ID}}">Open the application</a></p>{{template "foot" .}}{{end}}

{{define "settings"}}{{template "head" .}}
<form method="post" action="/portal/applications/{{.App.ID}}/settings"><input type="hidden" name="csrf_token" value="{{.CSRF}}">
<p><label>Name<br><input name="name" value="{{.App.Name}}" maxlength="64"></label></p>
<p><label>Description<br><textarea name="description" rows="2">{{.App.Description}}</textarea></label></p>
<p><label>OAuth2 redirect URIs (one per line)<br><textarea name="redirect_uris" rows="3">{{.App.RedirectURIs}}</textarea></label></p>
<p><label>Interactions endpoint URL<br><input name="endpoint_url" value="{{.Endpoint}}"></label></p>
<p><button type="submit">Save settings</button></p>
</form>{{template "foot" .}}{{end}}

{{define "token"}}{{template "head" .}}
<p>New bot token (shown once):</p>
<p><code id="bot-token">{{.Flash}}</code></p>
<p><a href="/portal/applications/{{.App.ID}}">Back to application</a></p>{{template "foot" .}}{{end}}
`))

// portalHandler serves the developer portal.
func (s *Server) portalHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/portal")
		path = strings.TrimSuffix(path, "/")
		switch {
		case path == "" || path == "/":
			u := s.portalUser(r)
			if u == nil {
				http.Redirect(w, r, "/portal/login", http.StatusFound)
				return
			}
			apps, err := s.Store.ApplicationsForOwner(u.ID)
			if err != nil {
				s.portalError(w, err)
				return
			}
			s.renderPortal(w, "dashboard", portalPage{Title: "Your applications", User: u, Apps: apps, CSRF: s.csrfToken(w, r)})
		case path == "/login":
			if r.Method == http.MethodPost {
				if !s.checkCSRF(r) {
					http.Error(w, "invalid csrf token", http.StatusForbidden)
					return
				}
				if err := r.ParseForm(); err != nil {
					s.portalError(w, ErrInvalidForm)
					return
				}
				user, err := s.Store.UserByLogin(r.FormValue("login"))
				if err != nil {
					s.renderPortal(w, "login", portalPage{Title: "Sign in", Error: "Invalid credentials", CSRF: s.csrfToken(w, r)})
					return
				}
				hash, err := s.Store.PasswordHash(user.ID)
				if err != nil || !VerifyPassword(r.FormValue("password"), hash) {
					s.renderPortal(w, "login", portalPage{Title: "Sign in", Error: "Invalid credentials", CSRF: s.csrfToken(w, r)})
					return
				}
				token, err := NewToken()
				if err != nil {
					s.portalError(w, err)
					return
				}
				if _, err := s.Store.CreateSession(user.ID, TokenHash(token), "portal"); err != nil {
					s.portalError(w, err)
					return
				}
				s.setPortalCookie(w, r, portalCookie, token)
				http.Redirect(w, r, "/portal", http.StatusFound)
				return
			}
			s.renderPortal(w, "login", portalPage{Title: "Sign in", CSRF: s.csrfToken(w, r)})
		case path == "/logout":
			if cookie, err := r.Cookie(portalCookie); err == nil {
				if sess, _, err := s.Store.SessionByToken(TokenHash(cookie.Value)); err == nil {
					_ = s.Store.DeleteSession(sess.UserID, sess.ID)
				}
			}
			secure := s.cfg.PortalCookieSecure || r.TLS != nil
			http.SetCookie(w, &http.Cookie{Name: portalCookie, Value: "", Path: "/portal", MaxAge: -1, HttpOnly: true, Secure: secure})
			http.Redirect(w, r, "/portal/login", http.StatusFound)
		case path == "/applications/new":
			u := s.portalUser(r)
			if u == nil {
				http.Redirect(w, r, "/portal/login", http.StatusFound)
				return
			}
			s.renderPortal(w, "newapp", portalPage{Title: "New application", User: u, CSRF: s.csrfToken(w, r)})
		case path == "/applications":
			u := s.portalUser(r)
			if u == nil {
				http.Redirect(w, r, "/portal/login", http.StatusFound)
				return
			}
			if r.Method != http.MethodPost {
				http.Redirect(w, r, "/portal/applications/new", http.StatusFound)
				return
			}
			if !s.checkCSRF(r) {
				http.Error(w, "invalid csrf token", http.StatusForbidden)
				return
			}
			if err := r.ParseForm(); err != nil {
				s.portalError(w, ErrInvalidForm)
				return
			}
			name := strings.TrimSpace(r.FormValue("name"))
			if name == "" || len([]rune(name)) > 64 {
				s.renderPortal(w, "newapp", portalPage{Title: "New application", User: u, Error: "Name must be 1-64 characters", CSRF: s.csrfToken(w, r)})
				return
			}
			app, err := s.Store.CreateApplication(u.ID, name, strings.TrimSpace(r.FormValue("description")))
			if err != nil {
				s.portalError(w, err)
				return
			}
			s.renderPortal(w, "secret", portalPage{Title: app.Name, User: u, App: &app,
				Flash: app.ClientSecret, CSRF: s.csrfToken(w, r)})
		case strings.HasPrefix(path, "/applications/"):
			s.portalApplication(w, r, strings.TrimPrefix(path, "/applications/"))
		default:
			http.NotFound(w, r)
		}
	})
}

// portalApplication serves the per-application pages and their form posts.
func (s *Server) portalApplication(w http.ResponseWriter, r *http.Request, rest string) {
	u := s.portalUser(r)
	if u == nil {
		http.Redirect(w, r, "/portal/login", http.StatusFound)
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	appID := parts[0]
	app, err := s.Store.Application(appID)
	if err != nil || app.OwnerID != u.ID {
		http.NotFound(w, r)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		commands, err := s.Store.ListCommands(app.ID, "")
		if err != nil {
			s.portalError(w, err)
			return
		}
		s.renderPortal(w, "app", portalPage{Title: app.Name, User: u, App: &app, Commands: commands, CSRF: s.csrfToken(w, r)})
	case action == "token" && r.Method == http.MethodPost:
		if !s.checkCSRF(r) {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		token, err := s.Store.ResetBotToken(app.ID, u.ID)
		if err != nil {
			s.portalError(w, err)
			return
		}
		s.renderPortal(w, "token", portalPage{Title: "Bot token", User: u, App: &app, Flash: token, CSRF: s.csrfToken(w, r)})
	case action == "settings" && r.Method == http.MethodGet:
		endpoint := ""
		if app.InteractionsEndpointURL != nil {
			endpoint = *app.InteractionsEndpointURL
		}
		s.renderPortal(w, "settings", portalPage{Title: app.Name + " settings", User: u, App: &app,
			Endpoint: endpoint, CSRF: s.csrfToken(w, r)})
	case action == "settings" && r.Method == http.MethodPost:
		if !s.checkCSRF(r) {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			s.portalError(w, ErrInvalidForm)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" || len([]rune(name)) > 64 {
			s.portalError(w, ErrInvalidForm)
			return
		}
		desc := strings.TrimSpace(r.FormValue("description"))
		var uris []string
		for _, line := range strings.Split(r.FormValue("redirect_uris"), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				uris = append(uris, line)
			}
		}
		endpoint := strings.TrimSpace(r.FormValue("endpoint_url"))
		var endpointPtr *string
		if endpoint != "" {
			endpointPtr = &endpoint
		}
		if _, err := s.Store.UpdateApplication(app.ID, u.ID, &name, &desc, &uris, nil, endpointPtr); err != nil {
			s.portalError(w, err)
			return
		}
		http.Redirect(w, r, "/portal/applications/"+app.ID, http.StatusFound)
	case action == "commands" && r.Method == http.MethodPost:
		if !s.checkCSRF(r) {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			s.portalError(w, ErrInvalidForm)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" || len([]rune(name)) > 32 {
			s.portalError(w, ErrInvalidForm)
			return
		}
		if _, err := s.Store.CreateCommand(app.ID, "", store.CommandInput{
			Name: name, Description: strings.TrimSpace(r.FormValue("description")),
		}); err != nil {
			s.portalError(w, err)
			return
		}
		http.Redirect(w, r, "/portal/applications/"+app.ID, http.StatusFound)
	case action == "permissions" && r.Method == http.MethodGet:
		guildID := r.URL.Query().Get("guild_id")
		perms, err := s.Store.ListCommandPermissions(app.ID, guildID)
		if err != nil {
			s.portalError(w, err)
			return
		}
		rows := make([]portalPermRow, 0)
		for _, cp := range perms {
			for _, entry := range cp.Permissions {
				rows = append(rows, portalPermRow{Command: cp.ID, Target: entry.ID, Type: entry.Type, Allow: entry.Permission})
			}
		}
		s.renderPortal(w, "app", portalPage{Title: app.Name, User: u, App: &app, GuildID: guildID, PermRows: rows, CSRF: s.csrfToken(w, r)})
	case action == "permissions" && r.Method == http.MethodPost:
		if !s.checkCSRF(r) {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			s.portalError(w, ErrInvalidForm)
			return
		}
		guildID := strings.TrimSpace(r.FormValue("guild_id"))
		commandID := strings.TrimSpace(r.FormValue("command_id"))
		targetID := strings.TrimSpace(r.FormValue("target_id"))
		if guildID == "" || commandID == "" || targetID == "" {
			s.portalError(w, ErrInvalidForm)
			return
		}
		typ := atoiSafe(r.FormValue("type"))
		allow := strings.EqualFold(r.FormValue("allow"), "true")
		if _, err := s.Store.PutCommandPermissions(app.ID, guildID, commandID, []store.CommandPermissionEntry{
			{ID: targetID, Type: typ, Permission: allow},
		}); err != nil {
			s.portalError(w, err)
			return
		}
		http.Redirect(w, r, "/portal/applications/"+app.ID+"/permissions?guild_id="+guildID, http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) portalUser(r *http.Request) *model.User {
	cookie, err := r.Cookie(portalCookie)
	if err != nil || cookie.Value == "" {
		return nil
	}
	_, user, err := s.Store.SessionByToken(TokenHash(cookie.Value))
	if err != nil {
		return nil
	}
	return &user
}

func (s *Server) renderPortal(w http.ResponseWriter, name string, page portalPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := portalTemplates.ExecuteTemplate(w, name, page); err != nil {
		s.cfg.Logger.Error("portal render", "err", err)
	}
}

func (s *Server) portalError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if apiErr, ok := err.(*APIError); ok {
		status = apiErr.Status
	}
	http.Error(w, http.StatusText(status), status)
}
