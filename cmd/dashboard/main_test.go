package main

import "testing"

func TestDemoIgnoresLiveAuthEnvironment(t *testing.T) {
	t.Setenv("DASHBOARD_ADMIN_TOKEN_FILE", "nonexistent-secret")
	t.Setenv("DASHBOARD_OIDC_ISSUER", "http://invalid-issuer")
	t.Setenv("DASHBOARD_PUBLIC_ORIGIN", "https://production.example")
	cfg, e := authConfigFromEnv(true)
	if e != nil || !cfg.Demo || cfg.Issuer != "" || cfg.AdminToken != "" || cfg.PublicOrigin != "" {
		t.Fatal("demo read live auth configuration", e)
	}
	if _, e = authConfigFromEnv(false); e == nil {
		t.Fatal("live mode ignored invalid secret configuration")
	}
}
