package api

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

// TestPortalCookieSecurity proves the portal cookies are HttpOnly, SameSite=Lax and
// only marked Secure for TLS requests or when the config forces it.
func TestPortalCookieSecurity(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/portal", nil)
	s := &Server{cfg: Config{}}
	s.setPortalCookie(w, r, portalCookie, "token-value")
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d", len(cookies))
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != 2 || c.Secure {
		t.Fatalf("plain GET cookie attrs: %+v", c)
	}

	// TLS request implies Secure without configuration.
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/portal", nil)
	r.TLS = &tls.ConnectionState{}
	s.setPortalCookie(w, r, portalCookie, "token-value")
	if !w.Result().Cookies()[0].Secure {
		t.Fatal("TLS request cookie is not Secure")
	}

	// Forced configuration marks it Secure even over plain HTTP.
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/portal", nil)
	forced := &Server{cfg: Config{PortalCookieSecure: true}}
	forced.setPortalCookie(w, r, portalCookie, "token-value")
	if !w.Result().Cookies()[0].Secure {
		t.Fatal("forced Secure cookie is not Secure")
	}
}
