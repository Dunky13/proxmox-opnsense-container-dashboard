package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"container-dashboard/internal/dashboard"
	"container-dashboard/internal/frontend"
	"container-dashboard/web"
)

func secret(name string) (string, error) {
	value := os.Getenv(name)
	file := os.Getenv(name + "_FILE")
	if value != "" && file != "" {
		return "", fmt.Errorf("configure only one source for %s", name)
	}
	if file != "" {
		info, e := os.Stat(file)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
			return "", fmt.Errorf("%s secret file must be a private regular file", name)
		}
		b, e := os.ReadFile(file)
		if e != nil {
			return "", fmt.Errorf("cannot read %s secret file", name)
		}
		value = strings.TrimSpace(string(b))
	}
	if len(value) > 8192 || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("invalid %s secret", name)
	}
	return value, nil
}
func list(name string) []string {
	out := []string{}
	for _, v := range strings.Split(os.Getenv(name), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func authConfigFromEnv(demo bool) (dashboard.AuthConfig, error) {
	if demo {
		return dashboard.AuthConfig{Demo: true}, nil
	}
	token, e := secret("DASHBOARD_ADMIN_TOKEN")
	if e != nil {
		return dashboard.AuthConfig{}, e
	}
	clientSecret, e := secret("DASHBOARD_OIDC_CLIENT_SECRET")
	if e != nil {
		return dashboard.AuthConfig{}, e
	}
	return dashboard.AuthConfig{AdminToken: token, PublicOrigin: os.Getenv("DASHBOARD_PUBLIC_ORIGIN"), Issuer: os.Getenv("DASHBOARD_OIDC_ISSUER"), ClientID: os.Getenv("DASHBOARD_OIDC_CLIENT_ID"), ClientSecret: clientSecret, RedirectURL: os.Getenv("DASHBOARD_OIDC_REDIRECT_URL"), Subjects: list("DASHBOARD_OIDC_SUBJECTS"), Emails: list("DASHBOARD_OIDC_EMAILS")}, nil
}

func run() error {
	demo := flag.Bool("demo", false, "use isolated mock providers; never contact infrastructure")
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP bind address")
	dir := flag.String("state-dir", "", "private durable state directory")
	flag.Parse()
	if *dir == "" {
		if *demo {
			*dir = "var/demo"
		} else {
			*dir = "var/live"
		}
	}
	authSettings, e := authConfigFromEnv(*demo)
	if e != nil {
		return e
	}
	auth, e := dashboard.NewAuth(context.Background(), authSettings)
	if e != nil {
		return e
	}
	store, e := dashboard.OpenStore(*dir, *demo)
	if e != nil {
		return e
	}
	defer store.Close()
	var providers dashboard.Providers = &dashboard.RealProviders{SSH: dashboard.OpenSSH{}}
	if *demo {
		providers, e = dashboard.NewDemo(store)
		if e != nil {
			return e
		}
		log.Print("DEMO mode: mock providers and isolated state; authentication bypassed")
	}
	service := dashboard.NewService(store, providers)
	defer service.Close()
	assets, e := web.Assets()
	if e != nil {
		return fmt.Errorf("embedded frontend: %w", e)
	}
	ui, e := frontend.New(assets)
	if e != nil {
		return e
	}
	server := &http.Server{Addr: *listen, Handler: dashboard.NewAPI(service, auth, ui), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("container dashboard listening on %s (demo=%t)", *listen, *demo)
	select {
	case e := <-done:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := server.Shutdown(shutdown); e != nil {
			_ = server.Close()
			return e
		}
	}
	return nil
}
func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}
