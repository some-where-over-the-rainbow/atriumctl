// Package console is the architect console (docs/architect-console.md):
// a web application served by the kernel over HTTPS with a self-signed
// certificate and token login (decisions 47, 49), listening only on the
// machine's loopback and home-network addresses.
//
// Views (slice 1): run, gate, needs, trace graph, history, baseline. The
// browser lints and diffs with the same needs core as agents and checks
// (needs_wasm.wasm). Gate answers, decisions and resumes go through the
// kernel under the architect's identity, like the command line.
package console

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"atriumctl/kernel/def"
)

//go:embed static
var static embed.FS

type Server struct {
	DataDir  string
	Def      func() *def.Swarm
	WasmPath string // needs_wasm.wasm; the console lints without it on the server only
	Auth     *Auth
	Log      func(format string, a ...any)

	mu      sync.Mutex
	drivers map[string]*driving
}

func (s *Server) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log(f, a...)
	}
}

func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self'; "+
		"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
}

// Handler is the console's HTTP handler.
func (s *Server) Handler() http.Handler {
	if s.drivers == nil {
		s.drivers = map[string]*driving{}
	}
	sub, _ := fs.Sub(static, "static")
	files := http.FileServer(http.FS(sub))
	mux := http.NewServeMux()
	mux.Handle("GET /", files)
	mux.HandleFunc("GET /needs_wasm.wasm", func(w http.ResponseWriter, r *http.Request) {
		if s.WasmPath == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/wasm")
		http.ServeFile(w, r, s.WasmPath)
	})
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	api := http.NewServeMux()
	api.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"ok": true}) })
	api.HandleFunc("GET /api/runs", s.listRuns)
	api.HandleFunc("GET /api/runs/{id}/state", s.state)
	api.HandleFunc("GET /api/runs/{id}/artifacts/{name}", s.artifact)
	api.HandleFunc("GET /api/runs/{id}/files", s.files)
	api.HandleFunc("GET /api/runs/{id}/snapshots", s.snapshots)
	api.HandleFunc("GET /api/runs/{id}/snapshots/{name}", s.snapshot)
	api.HandleFunc("GET /api/runs/{id}/events", s.events)
	api.HandleFunc("GET /api/runs/{id}/probes/{name}", s.probe)
	api.HandleFunc("POST /api/runs/{id}/gate", s.gate)
	api.HandleFunc("POST /api/runs/{id}/decide", s.decide)
	api.HandleFunc("POST /api/runs/{id}/resume", s.resume)
	api.HandleFunc("POST /api/runs/{id}/stop", s.stop)
	guarded := s.requireSession(api)
	mux.Handle("GET /api/", guarded)
	mux.Handle("POST /api/", guarded)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		mux.ServeHTTP(w, r)
	})
}

// requireSession guards the API; state-changing requests also need the
// X-Atriumctl header (not sendable cross-site without CORS) and a same-origin
// Origin when the browser sends one.
func (s *Server) requireSession(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Auth.Valid(r) {
			httpError(w, http.StatusUnauthorized, "sign in")
			return
		}
		if r.Method != http.MethodGet && !sameOrigin(r) {
			httpError(w, http.StatusForbidden, "cross-site request refused")
			return
		}
		h.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	if r.Header.Get("X-Atriumctl") != "1" {
		return false
	}
	o := r.Header.Get("Origin")
	return o == "" || o == "https://"+r.Host
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		httpError(w, http.StatusForbidden, "cross-site request refused")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	sid, err := s.Auth.Login(body.Token)
	switch {
	case errors.Is(err, errTooMany):
		httpError(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		s.logf("console: failed sign-in from %s", r.RemoteAddr)
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}
	s.logf("console: sign-in from %s", r.RemoteAddr)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: sid, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil && sameOrigin(r) {
		s.Auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// Serve listens with TLS on port at every given address until ctx ends.
func (s *Server) Serve(ctx context.Context, cert tls.Certificate, addrs []net.IP, port int) error {
	if len(addrs) == 0 {
		return errNoAddrs
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, len(addrs))
	for _, ip := range addrs {
		addr := net.JoinHostPort(ip.String(), strconv.Itoa(port))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		go func() { errc <- srv.ServeTLS(ln, "", "") }()
	}
	select {
	case <-ctx.Done():
		s.stopAll()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errc:
		return err
	}
}

// URLs lists how to reach the console.
func URLs(addrs []net.IP, port int) []string {
	var out []string
	host, _ := os.Hostname()
	for _, ip := range addrs {
		if ip.IsLoopback() {
			continue
		}
		out = append(out, "https://"+net.JoinHostPort(ip.String(), strconv.Itoa(port))+"/")
	}
	if host != "" {
		out = append(out, "https://"+host+".local:"+strconv.Itoa(port)+"/")
	}
	out = append(out, "https://localhost:"+strconv.Itoa(port)+"/")
	return out
}
