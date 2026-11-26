package api_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func portalClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func portalGet(t *testing.T, c *http.Client, base, path string) (int, string) {
	t.Helper()
	resp, err := c.Get(base + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

var csrfField = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// portalCSRF fetches a page (setting the CSRF cookie) and returns its form token.
func portalCSRF(t *testing.T, c *http.Client, base, path string) string {
	t.Helper()
	_, body := portalGet(t, c, base, path)
	m := csrfField.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf token on %s", path)
	}
	return m[1]
}

func portalPost(t *testing.T, c *http.Client, base, path string, form url.Values) (int, string) {
	t.Helper()
	resp, err := c.PostForm(base+path, form)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// TestDeveloperPortal covers login, application creation, command creation,
// permission editing, token reset, ownership and logout.
func TestDeveloperPortal(t *testing.T) {
	h := newHarness(t)
	alice := h.register("portalalice")
	h.register("portalbob")
	base := strings.TrimSuffix(h.server.URL, "/")

	// Unauthenticated access redirects to login.
	c := portalClient(t)
	if status, body := portalGet(t, c, base, "/portal"); status != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("unauth dashboard status %d", status)
	}
	if status, body := portalGet(t, c, base, "/portal/login"); status != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("login page %d", status)
	}
	// Bad credentials are rejected.
	loginToken := portalCSRF(t, c, base, "/portal/login")
	if status, body := portalPost(t, c, base, "/portal/login", url.Values{
		"csrf_token": {loginToken}, "login": {"portalalice@example.test"}, "password": {"wrong password"},
	}); status != 200 || !strings.Contains(body, "Invalid credentials") {
		t.Fatalf("bad login %d", status)
	}
	// Successful login follows to the dashboard.
	loginToken = portalCSRF(t, c, base, "/portal/login")
	status, body := portalPost(t, c, base, "/portal/login", url.Values{
		"csrf_token": {loginToken}, "login": {"portalalice@example.test"}, "password": {testPassword},
	})
	if status != 200 || !strings.Contains(body, "portalalice") {
		t.Fatalf("dashboard after login %d", status)
	}
	// Create an application.
	appToken := portalCSRF(t, c, base, "/portal/applications/new")
	status, body = portalPost(t, c, base, "/portal/applications", url.Values{
		"csrf_token": {appToken}, "name": {"Portal App"}, "description": {"made in the portal"},
	})
	if status != 200 || !strings.Contains(body, "Portal App") {
		t.Fatalf("create application %d", status)
	}
	match := regexp.MustCompile(`/portal/applications/([0-9]+)`).FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("no application link in page: %s", body)
	}
	appID := match[1]
	// The dashboard lists it.
	if status, body := portalGet(t, c, base, "/portal"); status != 200 || !strings.Contains(body, "Portal App") {
		t.Fatalf("dashboard listing %d", status)
	}
	// Create a command.
	cmdToken := portalCSRF(t, c, base, "/portal/applications/"+appID)
	status, body = portalPost(t, c, base, "/portal/applications/"+appID+"/commands", url.Values{
		"csrf_token": {cmdToken}, "name": {"ping"}, "description": {"responds with pong"},
	})
	if status != 200 || !strings.Contains(body, "ping") {
		t.Fatalf("create command %d", status)
	}
	commands, err := h.store.ListCommands(appID, "")
	if err != nil || len(commands) != 1 {
		t.Fatalf("store commands: %v %d", err, len(commands))
	}
	// Set and read back a command permission.
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Portal Guild"}, &guild)
	permToken := portalCSRF(t, c, base, "/portal/applications/"+appID)
	status, body = portalPost(t, c, base, "/portal/applications/"+appID+"/permissions", url.Values{
		"csrf_token": {permToken}, "guild_id": {guild.ID}, "command_id": {commands[0].ID},
		"target_id": {"123456789012345678"}, "type": {"2"}, "allow": {"true"},
	})
	if status != 200 || !strings.Contains(body, "123456789012345678") {
		t.Fatalf("permission page %d", status)
	}
	// Reset bot token and see it once.
	tokenCSRF := portalCSRF(t, c, base, "/portal/applications/"+appID)
	status, body = portalPost(t, c, base, "/portal/applications/"+appID+"/token", url.Values{
		"csrf_token": {tokenCSRF},
	})
	if status != 200 || !strings.Contains(body, "bot-token") {
		t.Fatalf("token page %d", status)
	}
	tokenMatch := regexp.MustCompile(`<code id="bot-token">([^<]+)</code>`).FindStringSubmatch(body)
	if tokenMatch == nil || len(tokenMatch[1]) < 20 {
		t.Fatalf("no bot token shown: %s", body)
	}

	// Another account cannot see the application.
	bobClient := portalClient(t)
	bobToken := portalCSRF(t, bobClient, base, "/portal/login")
	portalPost(t, bobClient, base, "/portal/login", url.Values{
		"csrf_token": {bobToken}, "login": {"portalbob@example.test"}, "password": {testPassword},
	})
	if status, _ := portalGet(t, bobClient, base, "/portal/applications/"+appID); status != http.StatusNotFound {
		t.Fatalf("foreign application status %d", status)
	}

	// Forms without a CSRF token are refused.
	if status, _ := portalPost(t, c, base, "/portal/applications", url.Values{
		"name": {"No Token"},
	}); status != http.StatusForbidden {
		t.Fatalf("csrf-less post status %d", status)
	}
	// Logout invalidates the cookie.
	if status, body := portalGet(t, c, base, "/portal/logout"); status != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("logout status %d", status)
	}
	if status, body := portalGet(t, c, base, "/portal"); status != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("dashboard after logout status %d", status)
	}
}
