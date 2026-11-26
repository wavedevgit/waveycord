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
	if status, body := portalPost(t, c, base, "/portal/login", url.Values{
		"login": {"portalalice@example.test"}, "password": {"wrong password"},
	}); status != 200 || !strings.Contains(body, "Invalid credentials") {
		t.Fatalf("bad login %d", status)
	}
	// Successful login follows to the dashboard.
	status, body := portalPost(t, c, base, "/portal/login", url.Values{
		"login": {"portalalice@example.test"}, "password": {testPassword},
	})
	if status != 200 || !strings.Contains(body, "portalalice") {
		t.Fatalf("dashboard after login %d", status)
	}
	// Create an application.
	status, body = portalPost(t, c, base, "/portal/applications", url.Values{
		"name": {"Portal App"}, "description": {"made in the portal"},
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
	status, body = portalPost(t, c, base, "/portal/applications/"+appID+"/commands", url.Values{
		"name": {"ping"}, "description": {"responds with pong"},
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
	status, body = portalPost(t, c, base, "/portal/applications/"+appID+"/permissions", url.Values{
		"guild_id": {guild.ID}, "command_id": {commands[0].ID},
		"target_id": {"123456789012345678"}, "type": {"2"}, "allow": {"true"},
	})
	if status != 200 || !strings.Contains(body, "123456789012345678") {
		t.Fatalf("permission page %d", status)
	}
	// Reset bot token and see it once.
	status, body = portalPost(t, c, base, "/portal/applications/"+appID+"/token", url.Values{})
	if status != 200 || !strings.Contains(body, "bot-token") {
		t.Fatalf("token page %d", status)
	}
	tokenMatch := regexp.MustCompile(`<code id="bot-token">([^<]+)</code>`).FindStringSubmatch(body)
	if tokenMatch == nil || len(tokenMatch[1]) < 20 {
		t.Fatalf("no bot token shown: %s", body)
	}

	// Another account cannot see the application.
	bobClient := portalClient(t)
	portalPost(t, bobClient, base, "/portal/login", url.Values{
		"login": {"portalbob@example.test"}, "password": {testPassword},
	})
	if status, _ := portalGet(t, bobClient, base, "/portal/applications/"+appID); status != http.StatusNotFound {
		t.Fatalf("foreign application status %d", status)
	}

	// Logout invalidates the cookie.
	if status, body := portalGet(t, c, base, "/portal/logout"); status != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("logout status %d", status)
	}
	if status, body := portalGet(t, c, base, "/portal"); status != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("dashboard after logout status %d", status)
	}
}
