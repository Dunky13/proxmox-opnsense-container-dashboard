package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// APIAuth permits a parent application to replace the provisional adapters.
type APIAuth interface {
	Middleware(http.Handler) http.Handler
	Principal(*http.Request) (Principal, bool)
	Status(http.ResponseWriter, *http.Request)
	Login(http.ResponseWriter, *http.Request)
	Callback(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
	OriginAllowed(*http.Request, bool) bool
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, e error) {
	f := &Fault{"internal", "internal operation failed", 500}
	var typed *Fault
	if errors.As(e, &typed) {
		f = typed
	}
	writeJSON(w, f.Status, map[string]any{"error": f})
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if content := r.Header.Get("Content-Type"); !strings.HasPrefix(content, "application/json") {
		return bad("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	return decodeBounded(r.Body, v)
}
func NewAPI(s *Service, auth APIAuth, frontend http.Handler) http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, fn func(http.ResponseWriter, *http.Request) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if e := fn(w, r); e != nil {
				writeError(w, e)
			}
		})
	}
	mux.HandleFunc("GET /api/v1/auth/status", auth.Status)
	mux.HandleFunc("GET /api/v1/auth/login", auth.Login)
	mux.HandleFunc("GET /api/v1/auth/callback", auth.Callback)
	mux.HandleFunc("POST /api/v1/auth/logout", auth.Logout)
	handle("GET /api/v1/settings", func(w http.ResponseWriter, r *http.Request) error { writeJSON(w, 200, s.Settings()); return nil })
	handle("PUT /api/v1/settings", func(w http.ResponseWriter, r *http.Request) error {
		var req ConfigEdit
		if e := readJSON(w, r, &req); e != nil {
			return e
		}
		out, e := s.UpdateConfig(req)
		if e == nil {
			writeJSON(w, 200, out)
		}
		return e
	})
	handle("GET /api/v1/profiles", func(w http.ResponseWriter, r *http.Request) error { writeJSON(w, 200, DefaultProfiles()); return nil })
	handle("GET /api/v1/opnsenses/{id}/subnets", func(w http.ResponseWriter, r *http.Request) error {
		out, err := s.Subnets(r.Context(), r.PathValue("id"))
		if err == nil {
			writeJSON(w, 200, out)
		}
		return err
	})
	handle("GET /api/v1/opnsenses/{id}/access-lists", func(w http.ResponseWriter, r *http.Request) error {
		out, err := s.AccessLists(r.Context(), r.PathValue("id"))
		if err == nil {
			writeJSON(w, 200, out)
		}
		return err
	})
	handle("POST /api/v1/hosts/{id}/test", func(w http.ResponseWriter, r *http.Request) error {
		out, e := s.TestHost(r.Context(), r.PathValue("id"))
		if e == nil {
			writeJSON(w, 200, out)
		}
		return e
	})
	handle("POST /api/v1/hosts/{id}/containers/{vmid}/maintenance", func(w http.ResponseWriter, r *http.Request) error {
		if e := readJSON(w, r, &struct{}{}); e != nil {
			return e
		}
		vmid, e := strconv.Atoi(r.PathValue("vmid"))
		if e != nil {
			return bad("invalid VMID")
		}
		out, e := s.Maintenance(r.Context(), r.PathValue("id"), vmid)
		if e == nil {
			writeJSON(w, 200, out)
		}
		return e
	})
	handle("POST /api/v1/opnsenses/{id}/test", func(w http.ResponseWriter, r *http.Request) error {
		out, e := s.TestRouter(r.Context(), r.PathValue("id"))
		if e == nil {
			writeJSON(w, 200, out)
		}
		return e
	})
	handle("GET /api/v1/inventory", func(w http.ResponseWriter, r *http.Request) error {
		st, inv, _ := s.displayObservation(r.Context(), false)
		writeJSON(w, 200, decorateInventory(inv, st))
		return nil
	})
	handle("GET /api/v1/reconciliation", func(w http.ResponseWriter, r *http.Request) error {
		st, inv, routers := s.displayObservation(r.Context(), true)
		writeJSON(w, 200, reconcile(st, inv, routers))
		return nil
	})
	handle("POST /api/v1/plans", func(w http.ResponseWriter, r *http.Request) error {
		var req PlanRequest
		if e := readJSON(w, r, &req); e != nil {
			return e
		}
		out, e := s.Plan(r.Context(), req)
		if e == nil {
			writeJSON(w, 201, out)
		}
		return e
	})
	handle("POST /api/v1/plans/{id}/apply", func(w http.ResponseWriter, r *http.Request) error {
		if e := readJSON(w, r, &struct{}{}); e != nil {
			return e
		}
		out, e := s.ApplyPlan(r.Context(), r.PathValue("id"))
		if e == nil {
			writeJSON(w, 200, out)
		}
		return e
	})
	handle("GET /api/v1/operations", func(w http.ResponseWriter, r *http.Request) error { writeJSON(w, 200, s.Operations()); return nil })
	handle("POST /api/v1/operations/{id}/recover", func(w http.ResponseWriter, r *http.Request) error {
		if e := readJSON(w, r, &struct{}{}); e != nil {
			return e
		}
		out, e := s.Recover(r.Context(), r.PathValue("id"))
		if e == nil {
			writeJSON(w, 200, out)
		}
		return e
	})
	handle("POST /api/v1/jobs", func(w http.ResponseWriter, r *http.Request) error {
		var req JobRequest
		if e := readJSON(w, r, &req); e != nil {
			return e
		}
		out, e := s.StartJob(r.Context(), req)
		if e == nil {
			writeJSON(w, 202, out)
		}
		return e
	})
	handle("GET /api/v1/jobs", func(w http.ResponseWriter, r *http.Request) error { writeJSON(w, 200, s.Jobs()); return nil })
	handle("GET /api/v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) error {
		out, e := s.Job(r.PathValue("id"))
		if e == nil {
			writeJSON(w, 200, out)
		}
		return e
	})
	handle("GET /api/v1/jobs/{id}/logs", func(w http.ResponseWriter, r *http.Request) error {
		j, e := s.Job(r.PathValue("id"))
		if e == nil {
			writeJSON(w, 200, map[string]any{"jobId": j.ID, "logs": j.Logs, "truncated": j.Truncated})
		}
		return e
	})
	handle("POST /api/v1/jobs/{id}/acknowledge", func(w http.ResponseWriter, r *http.Request) error {
		if e := readJSON(w, r, &struct{}{}); e != nil {
			return e
		}
		j, e := s.AcknowledgeJob(r.PathValue("id"))
		if e == nil {
			writeJSON(w, 200, j)
		}
		return e
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, &Fault{"notFound", "API route not found", 404})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	if frontend != nil {
		mux.Handle("/", frontend)
	}
	return auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			public := r.Method == "GET" && (r.URL.Path == "/api/v1/auth/status" || r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/callback")
			p, ok := auth.Principal(r)
			if !public && !ok {
				writeError(w, &Fault{"unauthenticated", "administrator authentication is required", 401})
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" && !auth.OriginAllowed(r, p.Method == "oidc") {
				writeError(w, &Fault{"originRejected", "same-origin write required", 403})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
			defer cancel()
			ctx, closeHTTP := withProviderHTTP(ctx)
			defer closeHTTP()
			r = r.WithContext(ctx)
		}
		mux.ServeHTTP(w, r)
	}))
}
