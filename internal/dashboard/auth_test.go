package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestBearerAPIAuthBoundaries(t *testing.T) {
	s, _, _ := demoService(t)
	auth, e := NewAuth(context.Background(), AuthConfig{AdminToken: "setup-secret", PublicOrigin: "https://dashboard.example"})
	if e != nil {
		t.Fatal(e)
	}
	api := NewAPI(s, auth, nil)
	cases := []struct {
		method, path, body, token, origin, fetch string
		want                                     int
	}{
		{"GET", "/api/v1/auth/status", "", "", "", "", 200},
		{"GET", "/api/v1/settings", "", "", "", "", 401},
		{"GET", "/api/v1/settings", "", "wrong", "", "", 401},
		{"GET", "/api/v1/settings", "", "setup-secret", "", "", 200},
		{"POST", "/api/v1/plans", "{}", "setup-secret", "https://attacker.example", "", 403},
		{"POST", "/api/v1/plans", "{}", "setup-secret", "", "cross-site", 403},
		{"POST", "/api/v1/plans", "{\"command\":\"reboot\"}", "setup-secret", "https://dashboard.example", "", 400},
		{"POST", "/api/v1/plans", "{} {}", "setup-secret", "", "", 400},
		{"POST", "/api/v1/plans", strings.Repeat(" ", 1<<20) + "{}", "setup-secret", "", "", 400},
	}
	for _, tt := range cases {
		r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		r.Header.Set("Content-Type", "application/json")
		if tt.token != "" {
			r.Header.Set("Authorization", "Bearer "+tt.token)
		}
		if tt.origin != "" {
			r.Header.Set("Origin", tt.origin)
		}
		if tt.fetch != "" {
			r.Header.Set("Sec-Fetch-Site", tt.fetch)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != tt.want {
			t.Errorf("%s %s got %d: %s", tt.method, tt.path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "setup-secret") || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("secret leak or CORS")
		}
	}
	auth, e = NewAuth(context.Background(), AuthConfig{})
	if e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	NewAPI(s, auth, nil).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/inventory", nil))
	if w.Code != 401 {
		t.Fatal("default live access did not fail closed")
	}
}
func TestOIDCAllowlistNoncePKCEAndServerSessions(t *testing.T) {
	for _, tc := range []struct {
		name, subject, email              string
		verified, nonceWrong, wantAllowed bool
		subjects, emails                  []string
	}{
		{name: "explicit-subject", subject: "admin", subjects: []string{"admin"}, wantAllowed: true},
		{name: "issuer-user-denied", subject: "outsider", subjects: []string{"admin"}},
		{name: "verified-email", subject: "email-admin", email: "Admin@example.test", verified: true, emails: []string{"admin@example.test"}, wantAllowed: true},
		{name: "unverified-email-denied", subject: "email-admin", email: "admin@example.test", emails: []string{"admin@example.test"}},
		{name: "nonce-denied", subject: "admin", subjects: []string{"admin"}, nonceWrong: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, e := rsa.GenerateKey(rand.Reader, 2048)
			if e != nil {
				t.Fatal(e)
			}
			jwk := jose.JSONWebKey{Key: key, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}
			signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jwk}, nil)
			if e != nil {
				t.Fatal(e)
			}
			nonce, challenge := "", ""
			issuer := ""
			exchanges := 0
			provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					writeJSON(w, 200, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}})
				case "/keys":
					writeJSON(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
				case "/token":
					exchanges++
					if r.ParseForm() != nil {
						t.Error("bad token form")
					}
					hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
					if challenge == "" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
						t.Error("missing or wrong PKCE verifier")
					}
					if r.Form.Get("code") != "test-code" {
						t.Error("wrong code")
					}
					n := nonce
					if tc.nonceWrong {
						n = "different-nonce"
					}
					claims := map[string]any{"iss": issuer, "sub": tc.subject, "aud": "dashboard-client", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": n, "email": tc.email, "email_verified": tc.verified}
					raw, _ := json.Marshal(claims)
					signed, e := signer.Sign(raw)
					if e != nil {
						t.Error(e)
						return
					}
					compact, e := signed.CompactSerialize()
					if e != nil {
						t.Error(e)
						return
					}
					writeJSON(w, 200, map[string]any{"access_token": "not-saved", "token_type": "Bearer", "id_token": compact})
				default:
					http.NotFound(w, r)
				}
			}))
			defer provider.Close()
			issuer = provider.URL
			s, _, _ := demoService(t)
			var handler http.Handler
			apiServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
			defer apiServer.Close()
			auth, e := NewAuth(context.Background(), AuthConfig{Issuer: issuer, ClientID: "dashboard-client", ClientSecret: "client-secret", PublicOrigin: apiServer.URL, RedirectURL: apiServer.URL + "/api/v1/auth/callback", Subjects: tc.subjects, Emails: tc.emails, HTTPClient: provider.Client()})
			if e != nil {
				t.Fatal(e)
			}
			handler = NewAPI(s, auth, nil)
			client := apiServer.Client()
			client.Jar, _ = cookiejar.New(nil)
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			res, e := client.Get(apiServer.URL + "/api/v1/auth/login")
			if e != nil {
				t.Fatal(e)
			}
			res.Body.Close()
			if res.StatusCode != 302 {
				t.Fatalf("login %d", res.StatusCode)
			}
			u, e := url.Parse(res.Header.Get("Location"))
			if e != nil {
				t.Fatal(e)
			}
			state := u.Query().Get("state")
			nonce = u.Query().Get("nonce")
			challenge = u.Query().Get("code_challenge")
			if state == "" || nonce == "" || challenge == "" || u.Query().Get("code_challenge_method") != "S256" {
				t.Fatal("missing state, nonce or PKCE")
			}
			for _, cookie := range res.Cookies() {
				if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
					t.Fatal("unsafe session cookie")
				}
			}
			callback := apiServer.URL + "/api/v1/auth/callback?code=test-code&state=" + url.QueryEscape(state)
			res, e = client.Get(callback)
			if e != nil {
				t.Fatal(e)
			}
			res.Body.Close()
			if tc.wantAllowed && res.StatusCode != 303 {
				t.Fatalf("callback %d", res.StatusCode)
			}
			if !tc.wantAllowed && res.StatusCode != 403 {
				t.Fatalf("unauthorized issuer login %d", res.StatusCode)
			}
			res, e = client.Get(apiServer.URL + "/api/v1/settings")
			if e != nil {
				t.Fatal(e)
			}
			res.Body.Close()
			want := 401
			if tc.wantAllowed {
				want = 200
			}
			if res.StatusCode != want {
				t.Fatalf("session access %d", res.StatusCode)
			}
			res, e = client.Get(callback)
			if e != nil {
				t.Fatal(e)
			}
			res.Body.Close()
			if res.StatusCode != 403 || exchanges != 1 {
				t.Fatal("callback replay was accepted")
			}
			if tc.wantAllowed {
				req, _ := http.NewRequest("POST", apiServer.URL+"/api/v1/auth/logout", nil)
				res, e = client.Do(req)
				if e != nil {
					t.Fatal(e)
				}
				res.Body.Close()
				if res.StatusCode != 403 {
					t.Fatal("cookie write without origin accepted")
				}
				req, _ = http.NewRequest("POST", apiServer.URL+"/api/v1/auth/logout", nil)
				req.Header.Set("Origin", apiServer.URL)
				res, e = client.Do(req)
				if e != nil {
					t.Fatal(e)
				}
				res.Body.Close()
				if res.StatusCode != 200 {
					t.Fatal("same-origin logout failed")
				}
			}
		})
	}
}
func TestOIDCRequiresAdminAllowlist(t *testing.T) {
	_, e := NewAuth(context.Background(), AuthConfig{Issuer: "https://issuer.example", ClientID: "client", ClientSecret: "secret", PublicOrigin: "https://dashboard.example", RedirectURL: "https://dashboard.example/api/v1/auth/callback"})
	if e == nil {
		t.Fatal("issuer authentication alone granted admin")
	}
}

func TestOIDCRejectsInsecureSigningKeyEndpoint(t *testing.T) {
	issuer := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": "http://insecure.example/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	}))
	defer server.Close()
	issuer = server.URL
	_, e := NewAuth(context.Background(), AuthConfig{Issuer: issuer, ClientID: "client", ClientSecret: "secret", PublicOrigin: "https://dashboard.example", RedirectURL: "https://dashboard.example/api/v1/auth/callback", Subjects: []string{"admin"}, HTTPClient: server.Client()})
	if e == nil {
		t.Fatal("accepted insecure JWKS endpoint")
	}
}

func TestOIDCRejectsHTTPSDowngradeRedirect(t *testing.T) {
	hit := false
	insecure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true; writeJSON(w, 200, map[string]string{}) }))
	defer insecure.Close()
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, insecure.URL, http.StatusFound) }))
	defer issuer.Close()
	_, e := NewAuth(context.Background(), AuthConfig{Issuer: issuer.URL, ClientID: "client", ClientSecret: "secret", PublicOrigin: "https://dashboard.example", RedirectURL: "https://dashboard.example/api/v1/auth/callback", Subjects: []string{"admin"}, HTTPClient: issuer.Client()})
	if e == nil || hit {
		t.Fatal("followed HTTPS downgrade")
	}
}
