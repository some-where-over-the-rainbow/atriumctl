package main

// The egress proxy: the harness container's only way out. CONNECT to an
// allowlisted host:port is tunnelled; everything else is refused and logged.

import (
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type EgressEntry struct {
	Time    time.Time `json:"ts"`
	Method  string    `json:"method"`
	Target  string    `json:"target"`
	Allowed bool      `json:"allowed"`
}

type EgressProxy struct {
	Allow map[string]bool // host:port

	mu  sync.Mutex
	log []EgressEntry
}

func (p *EgressProxy) Log() []EgressEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]EgressEntry(nil), p.log...)
}

func (p *EgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	allowed := r.Method == http.MethodConnect && p.Allow[target]
	p.mu.Lock()
	p.log = append(p.log, EgressEntry{time.Now(), r.Method, target, allowed})
	p.mu.Unlock()
	if !allowed {
		http.Error(w, "egress denied by policy", http.StatusForbidden)
		return
	}
	up, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		return
	}
	w.WriteHeader(http.StatusOK)
	c, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	go func() {
		defer c.Close()
		defer up.Close()
		if n := buf.Reader.Buffered(); n > 0 {
			b, _ := buf.Reader.Peek(n)
			_, _ = up.Write(b)
		}
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
		go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
		<-done
	}()
}
