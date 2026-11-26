// Package api implements the documented Discord REST API over the store and
// dispatches gateway events for state changes.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/wavedevgit/waveycord/internal/gateway"
	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/store"
)

// Config holds the server knobs.
type Config struct {
	// GatewayURL is what GET /gateway advertises to clients.
	GatewayURL string
	// RateLimit is the number of requests per RateWindow per route bucket.
	RateLimit  int
	RateWindow time.Duration
	// MaxBodyBytes bounds JSON request bodies.
	MaxBodyBytes int64
	// MaxUploadBytes bounds one attachment upload.
	MaxUploadBytes int64
	// UploadDir is where attachment bytes are written.
	UploadDir string
	// CDNBase is the absolute base for attachment URLs.
	CDNBase string
	Logger  *slog.Logger
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		GatewayURL:     "ws://127.0.0.1:8080/gateway",
		RateLimit:      50,
		RateWindow:     time.Second,
		MaxBodyBytes:   8 << 20,
		MaxUploadBytes: 25 << 20,
		UploadDir:      "./uploads",
		CDNBase:        "http://127.0.0.1:8080",
		Logger:         slog.Default(),
	}
}

// Server serves /api/v10 and the gateway upgrade.
type Server struct {
	Store  *store.Store
	Hub    *gateway.Hub
	cfg    Config
	routes []route
	limit  *limiter
	rates  *messageRate
	raids  *raidTracker
	voice  *gateway.VoiceServer
	// reads coalesces identical concurrent reads, keyed by route and resource,
	// the same protection the published message-storage design calls request
	// coalescing.
	reads   singleflight.Group
	metrics *metrics
	started time.Time
}

// New builds a server. The hub may be nil in tests that do not touch gateways.
func New(st *store.Store, hub *gateway.Hub, cfg Config) *Server {
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = 50
	}
	if cfg.RateWindow <= 0 {
		cfg.RateWindow = time.Second
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 8 << 20
	}
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = 25 << 20
	}
	if cfg.UploadDir == "" {
		cfg.UploadDir = "./uploads"
	}
	if cfg.CDNBase == "" {
		cfg.CDNBase = "http://127.0.0.1:8080"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{Store: st, Hub: hub, cfg: cfg, limit: newLimiter(cfg.RateLimit, cfg.RateWindow),
		rates: newMessageRate(), raids: newRaidTracker(), metrics: newMetrics(), started: time.Now()}
	s.routes = s.buildRoutes()
	if hub != nil {
		hub.OnCluster(s.handleClusterEvent)
	}
	return s
}

// SessionStore exposes configuration for tests.
func (s *Server) Config() Config { return s.cfg }

// SetVoice mounts the voice gateway on /voice.
func (s *Server) SetVoice(v *gateway.VoiceServer) {
	s.voice = v
	if v != nil && s.Hub != nil {
		v.SetPublisher(s.Hub.PublishCluster)
		s.Hub.SetVoiceNotifier(v.MemberChanged)
	}
}

// Handler returns the root handler (API + gateway).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v10/", http.StripPrefix("/api/v10", s.apiHandler()))
	mux.Handle("/gateway", s.gatewayHandler())
	mux.Handle("/api/v10/gateway", s.gatewayHandler())
	if s.voice != nil {
		mux.Handle("/voice", s.voice.Handler())
		// Discord advertises a host; the client appends "/?token=...". Accept that
		// root upgrade without swallowing ordinary unmatched routes.
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && r.URL.Query().Get("token") != "" {
				s.voice.Handler().ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":0,"message":"404: Not Found"}`))
		}))
	}
	mux.Handle("/cdn/", http.StripPrefix("/cdn", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs := splitPath(r.URL.Path)
		if len(segs) < 3 {
			http.NotFound(w, r)
			return
		}
		c := &Ctx{W: w, R: r, Srv: s, Params: map[string]string{"channelID": segs[1], "uploadID": segs[2]}}
		s.handleCDN(c)
	})))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		status := "ok"
		code := http.StatusOK
		if err := s.Store.DB.PingContext(ctx); err != nil {
			status = "degraded"
			code = http.StatusServiceUnavailable
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": status, "dialect": s.Store.DB.Dialect.String(),
			"uptime_seconds": int(time.Since(s.started).Seconds()),
		})
	})
	mux.HandleFunc("/metrics", s.handleMetrics)
	return mux
}

// --- request context ---

// Ctx carries per-request state to handlers.
type Ctx struct {
	W   http.ResponseWriter
	R   *http.Request
	Srv *Server

	User      model.User
	SessionID string
	Token     string

	Params map[string]string
	route  *route
}

// Param returns a path parameter.
func (c *Ctx) Param(name string) string { return c.Params[name] }

// Bind decodes the request body into v.
func (c *Ctx) Bind(v any) error {
	if c.R.ContentLength == 0 {
		return errors.New("empty body")
	}
	body := http.MaxBytesReader(c.W, c.R.Body, c.Srv.cfg.MaxBodyBytes)
	dec := json.NewDecoder(body)
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// JSONStatus writes a status and JSON payload.
func (c *Ctx) JSONStatus(status int, v any) {
	c.W.Header().Set("Content-Type", "application/json")
	c.W.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(c.W).Encode(v); err != nil {
		c.Srv.cfg.Logger.Error("encode response", "err", err)
	}
}

// JSON writes a payload with status 200.
func (c *Ctx) JSON(v any) { c.JSONStatus(http.StatusOK, v) }

// NoContent writes 204.
func (c *Ctx) NoContent() { c.W.WriteHeader(http.StatusNoContent) }

// Fail writes a documented error envelope.
func (c *Ctx) Fail(status, code int, message string) {
	c.JSONStatus(status, map[string]any{"code": code, "message": message})
}

// FailForm writes a 50035 invalid form body response.
func (c *Ctx) FailForm(field string, errs ...string) {
	detail := map[string]any{}
	if len(errs) == 1 {
		detail["_errors"] = []map[string]any{{"code": "BASE_TYPE_INVALID", "message": errs[0]}}
	} else {
		list := make([]map[string]any, 0, len(errs))
		for _, e := range errs {
			list = append(list, map[string]any{"code": "BASE_TYPE_INVALID", "message": e})
		}
		detail["_errors"] = list
	}
	c.JSONStatus(http.StatusBadRequest, map[string]any{
		"code":    50035,
		"message": "Invalid Form Body",
		"errors":  map[string]any{field: detail},
	})
}

// --- errors ---

// APIError is an error with a documented status and JSON code.
type APIError struct {
	Status  int
	Code    int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// NewError builds an APIError.
func NewError(status, code int, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

// Documented error constructors.
var (
	ErrUnauthorized    = NewError(401, 0, "401: Unauthorized")
	ErrForbidden       = NewError(403, 50001, "Missing Access")
	ErrMissingPerms    = NewError(403, 50013, "Missing Permissions")
	ErrUnknownChannel  = NewError(404, 10003, "Unknown Channel")
	ErrUnknownGuild    = NewError(404, 10004, "Unknown Guild")
	ErrUnknownMember   = NewError(404, 10007, "Unknown Member")
	ErrUnknownMessage  = NewError(404, 10008, "Unknown Message")
	ErrUnknownUser     = NewError(404, 10013, "Unknown User")
	ErrUnknownInvite   = NewError(404, 10006, "Unknown Invite")
	ErrUnknownRole     = NewError(404, 10011, "Unknown Role")
	ErrUnknownEmoji    = NewError(404, 10014, "Unknown Emoji")
	ErrInvalidForm     = NewError(400, 50035, "Invalid Form Body")
	ErrEmptyMessage    = NewError(400, 50006, "Cannot send an empty message")
	ErrTooManyRequests = NewError(429, 0, "You are being rate limited.")
	ErrGuildOnly       = NewError(400, 50020, "This endpoint is only valid for guild channels")
	ErrDMOnly          = NewError(400, 50020, "This endpoint is only valid for direct message channels")
	ErrMaxGuilds       = NewError(400, 30001, "Maximum number of guilds reached (200)")
	ErrMaxRoles        = NewError(400, 30005, "Maximum number of roles reached (250)")
	ErrMaxEmojis       = NewError(400, 30008, "Maximum number of emojis reached")
	ErrUnknownBan      = NewError(404, 10026, "Unknown Ban")
	ErrUnknownWebhook  = NewError(404, 10015, "Unknown Webhook")
	ErrCannotDM        = NewError(403, 50007, "Cannot send messages to this user")
	ErrInvalidToken    = NewError(400, 40001, "Invalid Authorization")
	// Poll failures. Discord's exact codes for these are not published; both use
	// the generic form-body code, which is documented as the validation fallback.
	ErrPollEnded          = NewError(400, 50035, "Poll has ended")
	ErrPollAnswerNotFound = NewError(400, 50035, "Invalid Form Body")
	ErrBotsCannotVote     = NewError(403, 50001, "Bots cannot vote on polls")
)

func fail(c *Ctx, err error) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		c.JSONStatus(apiErr.Status, map[string]any{"code": apiErr.Code, "message": apiErr.Message})
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSONStatus(404, map[string]any{"code": 10000, "message": "Unknown entity"})
	case errors.Is(err, store.ErrConflict):
		c.JSONStatus(400, map[string]any{"code": 50035, "message": "Invalid Form Body"})
	default:
		c.Srv.cfg.Logger.Error("unhandled", "err", err)
		c.JSONStatus(500, map[string]any{"code": 0, "message": "Internal Server Error"})
	}
}

// --- router ---

type handlerFunc func(*Ctx)

type route struct {
	method  string
	pattern string // e.g. /channels/{channelID}/messages
	parts   []string
	h       handlerFunc
	auth    bool
}

func (s *Server) route(method, pattern string, auth bool, h handlerFunc) {
	s.routes = append(s.routes, route{
		method:  method,
		pattern: pattern,
		parts:   splitPath(pattern),
		h:       h,
		auth:    auth,
	})
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// match finds the best route: most static segments wins, so a literal path
// always beats a parameter at the same position.
func (s *Server) match(method, path string) (*route, map[string]string, bool) {
	segs := splitPath(path)
	var (
		best      *route
		bestParam map[string]string
		bestScore = -1
		pathKnown bool
	)
	for i := range s.routes {
		r := &s.routes[i]
		if len(r.parts) != len(segs) {
			continue
		}
		params := map[string]string{}
		ok := true
		score := 0
		for j, part := range r.parts {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				params[strings.Trim(part, "{}")] = segs[j]
				continue
			}
			if part != segs[j] {
				ok = false
				break
			}
			score++
		}
		if !ok {
			continue
		}
		pathKnown = true
		if r.method != method {
			continue
		}
		if score > bestScore {
			best, bestParam, bestScore = r, params, score
		}
	}
	return best, bestParam, pathKnown
}

func (s *Server) apiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		w.Header().Set("X-RateLimit-Limit", itoa(s.cfg.RateLimit))
		segs := splitPath(r.URL.EscapedPath())
		decoded := make([]string, len(segs))
		for i, seg := range segs {
			if v, err := url.PathUnescape(seg); err == nil {
				decoded[i] = v
			} else {
				decoded[i] = seg
			}
		}
		path := "/" + strings.Join(decoded, "/")
		rt, params, known := s.match(r.Method, path)
		if rt == nil {
			if known {
				w.Header().Set("Allow", s.allowed(path))
				w.WriteHeader(http.StatusMethodNotAllowed)
				_, _ = w.Write([]byte(`{"code":0,"message":"405: Method Not Allowed"}`))
			} else {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":0,"message":"404: Not Found"}`))
			}
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		c := &Ctx{W: rec, R: r, Srv: s, Params: params, route: rt}
		defer func() {
			if rec := recover(); rec != nil {
				s.cfg.Logger.Error("panic", "route", rt.pattern, "err", rec)
				c.JSONStatus(500, map[string]any{"code": 0, "message": "Internal Server Error"})
			}
			s.cfg.Logger.Debug("request", "method", r.Method, "route", rt.pattern, "ms", time.Since(started).Milliseconds())
		}()

		if rt.auth {
			token, user, sessionID, err := s.authenticate(r)
			if err != nil {
				c.Fail(err.(*APIError).Status, err.(*APIError).Code, err.(*APIError).Message)
				return
			}
			c.User, c.SessionID, c.Token = user, sessionID, token
			if !s.limit.allow(token, rt.pattern, w) {
				c.JSONStatus(429, map[string]any{
					"message":     "You are being rate limited.",
					"retry_after": s.cfg.RateWindow.Seconds(),
					"global":      false,
					"code":        0,
				})
				return
			}
		}
		rt.h(c)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		s.metrics.observe(r.Method, rt.pattern, status)
	})
}

func (s *Server) allowed(path string) string {
	seen := map[string]bool{}
	var out []string
	for _, r := range s.routes {
		segs := splitPath(path)
		if len(r.parts) != len(segs) {
			continue
		}
		ok := true
		for i, part := range r.parts {
			if strings.HasPrefix(part, "{") {
				continue
			}
			if part != segs[i] {
				ok = false
				break
			}
		}
		if ok && !seen[r.method] {
			seen[r.method] = true
			out = append(out, r.method)
		}
	}
	return strings.Join(out, ", ")
}

// authenticate resolves the Authorization header to a session.
func (s *Server) authenticate(r *http.Request) (string, model.User, string, error) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return "", model.User{}, "", ErrUnauthorized
	}
	isBot, isBearer := false, false
	switch {
	case strings.HasPrefix(raw, "Bot "):
		isBot = true
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "Bot "))
	case strings.HasPrefix(raw, "Bearer "):
		isBearer = true
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
	case strings.HasPrefix(raw, "User "):
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "User "))
	}
	if raw == "" {
		return "", model.User{}, "", ErrUnauthorized
	}
	if isBot {
		user, app, err := s.Store.BotSession(TokenHash(raw))
		if err != nil {
			return "", model.User{}, "", ErrUnauthorized
		}
		return raw, user, "bot:" + app.ID, nil
	}
	sess, user, err := s.Store.SessionByToken(TokenHash(raw))
	if err == nil {
		s.Store.TouchSession(sess.ID)
		return raw, user, sess.ID, nil
	}
	// OAuth2 access tokens are bearer tokens too; the docs treat them as user
	// authentication with the granted scopes.
	if isBearer {
		token, oauthUser, oauthErr := s.Store.TokenByAccessHash(TokenHash(raw))
		if oauthErr == nil {
			return raw, oauthUser, "oauth:" + token.ID, nil
		}
	}
	return "", model.User{}, "", ErrUnauthorized
}

// TokenHash is how tokens are stored: SHA-256 hex, never plaintext.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// --- rate limiting ---

type limitBucket struct {
	remaining int
	reset     time.Time
}

type limiter struct {
	mu      sync.Mutex
	buckets map[string]*limitBucket
	limit   int
	window  time.Duration
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{buckets: map[string]*limitBucket{}, limit: limit, window: window}
}

// allow reports whether the request fits, and always writes the headers.
func (l *limiter) allow(identity, routeName string, w http.ResponseWriter) bool {
	key := identity + "|" + routeName
	nowT := time.Now()
	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok || nowT.After(b.reset) {
		b = &limitBucket{remaining: l.limit, reset: nowT.Add(l.window)}
		l.buckets[key] = b
	}
	if b.remaining > 0 {
		b.remaining--
	}
	remaining, reset := b.remaining, b.reset
	l.mu.Unlock()

	h := w.Header()
	h.Set("X-RateLimit-Limit", itoa(l.limit))
	h.Set("X-RateLimit-Remaining", itoa(remaining))
	h.Set("X-RateLimit-Reset", reset.Format("2006-01-02T15:04:05.000Z07:00"))
	h.Set("X-RateLimit-Reset-After", formatSeconds(reset.Sub(nowT)))
	h.Set("X-RateLimit-Bucket", bucketID(routeName))
	return remaining > 0
}

func formatSeconds(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return strings.TrimRight(strings.TrimRight(itoa(int(d.Milliseconds()))+"."+pad3(int(d.Milliseconds())%1000), "0"), ".")
}

func pad3(n int) string {
	s := itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

func bucketID(routeName string) string {
	sum := sha256.Sum256([]byte(routeName))
	return hex.EncodeToString(sum[:8])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
