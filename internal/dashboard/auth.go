package dashboard

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type AuthConfig struct {
	Demo         bool
	AdminToken   string
	PublicOrigin string
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Subjects     []string
	Emails       []string
	HTTPClient   *http.Client
}
type Auth struct {
	config      AuthConfig
	sessions    *scs.SessionManager
	oauth       *oauth2.Config
	verifier    *oidc.IDTokenVerifier
	oidcContext context.Context
	httpClient  *http.Client
}
type Principal struct {
	ID     string `json:"id"`
	Method string `json:"method"`
}
type AuthStatus struct {
	Demo          bool   `json:"demo"`
	BearerEnabled bool   `json:"bearerEnabled"`
	OIDCEnabled   bool   `json:"oidcEnabled"`
	Authenticated bool   `json:"authenticated"`
	Principal     string `json:"principal"`
}

func NewAuth(ctx context.Context, c AuthConfig) (*Auth, error) {
	a := &Auth{config: c}
	a.sessions = scs.New()
	a.sessions.Lifetime = 8 * time.Hour
	a.sessions.IdleTimeout = 30 * time.Minute
	a.sessions.Cookie.Name = "dashboard_session"
	a.sessions.Cookie.Secure = true
	a.sessions.Cookie.HttpOnly = true
	a.sessions.Cookie.SameSite = http.SameSiteLaxMode
	a.sessions.Cookie.Persist = false
	a.sessions.ErrorFunc = func(w http.ResponseWriter, r *http.Request, e error) {
		writeError(w, &Fault{"sessionUnavailable", "session unavailable", 503})
	}
	if c.PublicOrigin != "" {
		u, e := url.Parse(c.PublicOrigin)
		if e != nil || u.User != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, bad("invalid public origin")
		}
	}
	if c.Issuer != "" || c.ClientID != "" || c.ClientSecret != "" || c.RedirectURL != "" {
		if c.Demo {
			return nil, bad("OIDC is unavailable in isolated demo mode")
		}
		if !secureOIDCEndpoint(c.Issuer) || c.ClientID == "" || c.ClientSecret == "" || c.PublicOrigin == "" {
			return nil, bad("OIDC requires HTTPS issuer, client credentials and public origin")
		}
		u, e := url.Parse(c.RedirectURL)
		if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/v1/auth/callback" || u.Scheme+"://"+u.Host != c.PublicOrigin {
			return nil, bad("OIDC redirect URL must be the HTTPS callback at public origin")
		}
		if len(c.Subjects) == 0 && len(c.Emails) == 0 {
			return nil, bad("OIDC requires an explicit subject or verified-email admin allowlist")
		}
		for _, v := range append(append([]string{}, c.Subjects...), c.Emails...) {
			if v == "" || v == "*" {
				return nil, bad("OIDC allowlist entries must be explicit")
			}
		}
		client := c.HTTPClient
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		clientCopy := *client
		client = &clientCopy
		client.Timeout = 15 * time.Second
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || !secureOIDCEndpoint(req.URL.String()) {
				return errors.New("OIDC redirect is not permitted")
			}
			return nil
		}
		a.httpClient = client

		// Keep a cancellation-free client context for later verifier JWKS refreshes.
		a.oidcContext = oidc.ClientContext(context.Background(), client)
		discoveryCtx, cancel := context.WithTimeout(oidc.ClientContext(ctx, client), 20*time.Second)
		defer cancel()
		provider, e := oidc.NewProvider(discoveryCtx, c.Issuer)
		if e != nil {
			return nil, errors.New("OIDC discovery failed; verify issuer and TLS trust")
		}
		ep := provider.Endpoint()
		var metadata struct {
			JWKSURI string `json:"jwks_uri"`
		}
		if provider.Claims(&metadata) != nil {
			return nil, bad("invalid OIDC metadata")
		}
		if !secureOIDCEndpoint(ep.AuthURL) || !secureOIDCEndpoint(ep.TokenURL) || !secureOIDCEndpoint(metadata.JWKSURI) {
			return nil, bad("OIDC authorization, token and JWKS endpoints must use HTTPS")
		}

		a.verifier = provider.VerifierContext(a.oidcContext, &oidc.Config{ClientID: c.ClientID})
		a.oauth = &oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL, Endpoint: ep, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
	}
	return a, nil
}
func (a *Auth) Middleware(next http.Handler) http.Handler { return a.sessions.LoadAndSave(next) }
func (a *Auth) Principal(r *http.Request) (Principal, bool) {
	if a.config.Demo {
		return Principal{"demo", "demo"}, true
	}
	header := r.Header.Get("Authorization")
	if header != "" {
		token, ok := strings.CutPrefix(header, "Bearer ")
		if ok && a.config.AdminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(a.config.AdminToken)) == 1 {
			return Principal{"admin-token", "bearer"}, true
		}
		return Principal{}, false
	}
	if a.sessions.GetString(r.Context(), "adminSubject") != "" && a.sessions.GetInt64(r.Context(), "adminExpires") > time.Now().Unix() {
		return Principal{a.sessions.GetString(r.Context(), "adminSubject"), "oidc"}, true
	}
	return Principal{}, false
}
func (a *Auth) Status(w http.ResponseWriter, r *http.Request) {
	p, ok := a.Principal(r)
	writeJSON(w, 200, AuthStatus{a.config.Demo, a.config.AdminToken != "", a.oauth != nil, ok, p.ID})
}
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	if a.oauth == nil {
		writeError(w, &Fault{"authUnavailable", "OIDC is not configured", 503})
		return
	}
	if e := a.sessions.RenewToken(r.Context()); e != nil {
		writeError(w, &Fault{"sessionUnavailable", "session unavailable", 503})
		return
	}
	state, nonce, verifier := oauth2.GenerateVerifier(), oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	a.sessions.Put(r.Context(), "loginState", state)
	a.sessions.Put(r.Context(), "loginNonce", nonce)
	a.sessions.Put(r.Context(), "loginVerifier", verifier)
	a.sessions.Put(r.Context(), "loginAt", time.Now().Unix())
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	reject := func() { writeError(w, &Fault{"oidcRejected", "OIDC login was not authorized", 403}) }
	if a.oauth == nil {
		reject()
		return
	}
	state := a.sessions.GetString(r.Context(), "loginState")
	nonce := a.sessions.GetString(r.Context(), "loginNonce")
	verifier := a.sessions.GetString(r.Context(), "loginVerifier")
	at := a.sessions.GetInt64(r.Context(), "loginAt")
	for _, k := range []string{"loginState", "loginNonce", "loginVerifier", "loginAt"} {
		a.sessions.Remove(r.Context(), k)
	}
	if state == "" || nonce == "" || verifier == "" || r.URL.Query().Get("code") == "" || subtle.ConstantTimeCompare([]byte(state), []byte(r.URL.Query().Get("state"))) != 1 || time.Now().Unix()-at > 600 || time.Now().Unix() < at {
		reject()
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ctx = oidc.ClientContext(ctx, a.httpClient)
	token, e := a.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if e != nil {
		reject()
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		reject()
		return
	}
	id, e := a.verifier.Verify(ctx, raw)
	if e != nil || id.Nonce == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(nonce)) != 1 {
		reject()
		return
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if id.Claims(&claims) != nil {
		reject()
		return
	}
	allowed := false
	for _, v := range a.config.Subjects {
		if id.Subject == v {
			allowed = true
		}
	}
	if claims.EmailVerified {
		for _, v := range a.config.Emails {
			if strings.EqualFold(v, claims.Email) {
				allowed = true
			}
		}
	}
	if !allowed || id.Subject == "" {
		reject()
		return
	}
	if a.sessions.RenewToken(r.Context()) != nil {
		reject()
		return
	}
	a.sessions.Put(r.Context(), "adminSubject", id.Subject)
	a.sessions.Put(r.Context(), "adminExpires", id.Expiry.Unix())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if e := a.sessions.Destroy(r.Context()); e != nil {
		writeError(w, &Fault{"sessionUnavailable", "session unavailable", 503})
		return
	}
	writeJSON(w, 200, map[string]bool{"authenticated": false})
}
func (a *Auth) OriginAllowed(r *http.Request, cookie bool) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return !cookie
	}
	expected := a.config.PublicOrigin
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	return origin == expected
}

func secureOIDCEndpoint(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
