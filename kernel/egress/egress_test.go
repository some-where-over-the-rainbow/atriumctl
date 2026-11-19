package egress

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atriumctl/kernel/trace"
)

func TestOnlyAllowlistedConnect(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err == nil {
			_, _ = c.Write([]byte("hello from upstream\n"))
			c.Close()
		}
	}()
	sink := &trace.Memory{}
	p := &Proxy{Allow: map[string]bool{"api.anthropic.com:443": true}, Trace: sink,
		Dial: func(n, _ string) (net.Conn, error) { return net.Dial(n, up.Addr().String()) }}
	srv := httptest.NewServer(p)
	defer srv.Close()

	connect := func(target string) (int, string) {
		c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _ = c.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"))
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			return resp.StatusCode, ""
		}
		line, _ := br.ReadString('\n')
		return 200, line
	}
	if code, line := connect("api.anthropic.com:443"); code != 200 || line != "hello from upstream\n" {
		t.Fatalf("allowed: %d %q", code, line)
	}
	if code, _ := connect("example.com:443"); code != 403 {
		t.Fatalf("not allowlisted: %d", code)
	}
	resp, _ := http.Get(srv.URL + "/plain")
	if resp.StatusCode != 403 {
		t.Fatalf("plain request: %d", resp.StatusCode)
	}
	var log []string
	for _, e := range sink.Events("egress") {
		log = append(log, e.Fields["target"].(string)+"="+map[bool]string{true: "allowed", false: "denied"}[e.Fields["allowed"].(bool)])
	}
	if strings.Join(log[:2], ",") != "api.anthropic.com:443=allowed,example.com:443=denied" || len(log) != 3 {
		t.Fatalf("egress trace: %v", log)
	}
}
