// Package egress is the harness containers' only way out: an HTTP CONNECT
// proxy that tunnels to allowlisted host:port pairs and refuses and traces
// everything else.
package egress

import (
	"io"
	"net"
	"net/http"
	"time"

	"atriumctl/kernel/trace"
)

// Anthropic is the allowlist for Claude Code on the subscription.
var Anthropic = map[string]bool{"api.anthropic.com:443": true}

type Proxy struct {
	Allow map[string]bool
	Trace trace.Sink
	Dial  func(network, addr string) (net.Conn, error) // for tests; nil = net.Dial with timeout
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	allowed := r.Method == http.MethodConnect && p.Allow[target]
	if p.Trace != nil {
		p.Trace.Emit(trace.Event{Type: "egress", SchemaVersion: trace.SchemaVersion, Source: trace.SourceEgress,
			TsStart: time.Now(), Fields: map[string]any{"method": r.Method, "target": target, "allowed": allowed}})
	}
	if !allowed {
		http.Error(w, "egress denied by policy", http.StatusForbidden)
		return
	}
	dial := p.Dial
	if dial == nil {
		dial = func(n, a string) (net.Conn, error) { return net.DialTimeout(n, a, 10*time.Second) }
	}
	up, err := dial("tcp", target)
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
