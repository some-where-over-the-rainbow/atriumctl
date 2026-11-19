package console

import (
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"atriumctl/kernel/def"
	"atriumctl/kernel/qrspi"
)

func newServer(t *testing.T) (*httptest.Server, *http.Client, *Auth) {
	t.Helper()
	dir := t.TempDir()
	auth, created, err := LoadAuth(dir, false)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	if st, _ := os.Stat(filepath.Join(dir, "console-token")); st.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v", st.Mode())
	}
	s := &Server{DataDir: t.TempDir(), Def: func() *def.Swarm { return qrspi.Slice1() }, Auth: auth}
	srv := httptest.NewTLSServer(s.Handler())
	t.Cleanup(srv.Close)
	c := srv.Client()
	c.Jar, _ = cookiejar.New(nil)
	return srv, c, auth
}

func post(c *http.Client, url, body string, hdr map[string]string) *http.Response {
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		panic(err)
	}
	resp.Body.Close()
	return resp
}

var atriumctl = map[string]string{"X-Atriumctl": "1"}

func TestLoginAndSessions(t *testing.T) {
	srv, c, auth := newServer(t)
	get := func(p string) *http.Response {
		r, err := c.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r
	}
	if r := get("/"); r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("index: %d %q", r.StatusCode, r.Header.Get("Content-Security-Policy"))
	}
	if r := get("/api/runs"); r.StatusCode != 401 {
		t.Fatalf("api without session: %d", r.StatusCode)
	}
	if r := post(c, srv.URL+"/login", `{"token":"`+auth.Token()+`"}`, nil); r.StatusCode != 403 {
		t.Fatalf("login without X-Atriumctl: %d", r.StatusCode)
	}
	for i := 0; i < 5; i++ {
		if r := post(c, srv.URL+"/login", `{"token":"WRONG"}`, atriumctl); r.StatusCode != 401 {
			t.Fatalf("wrong token: %d", r.StatusCode)
		}
	}
	if r := post(c, srv.URL+"/login", `{"token":"`+auth.Token()+`"}`, atriumctl); r.StatusCode != 429 {
		t.Fatalf("after 5 failures: %d", r.StatusCode)
	}
	auth.failures = nil
	// Lower case and spaces are accepted: typed on a phone.
	typed := strings.ToLower(strings.ReplaceAll(auth.Token(), "-", " "))
	r := post(c, srv.URL+"/login", `{"token":"`+typed+`"}`, atriumctl)
	if r.StatusCode != 200 {
		t.Fatalf("login: %d", r.StatusCode)
	}
	ck := r.Cookies()[0]
	if !ck.Secure || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Name != cookieName {
		t.Fatalf("cookie %+v", ck)
	}
	if r := get("/api/runs"); r.StatusCode != 200 {
		t.Fatalf("api with session: %d", r.StatusCode)
	}
	// State-changing calls need X-Atriumctl and a same-origin Origin.
	if r := post(c, srv.URL+"/api/runs/20261118-1142-4d69/resume", `{}`, nil); r.StatusCode != 403 {
		t.Fatalf("POST without X-Atriumctl: %d", r.StatusCode)
	}
	if r := post(c, srv.URL+"/api/runs/20261118-1142-4d69/resume", `{}`, map[string]string{"X-Atriumctl": "1", "Origin": "https://evil.example"}); r.StatusCode != 403 {
		t.Fatalf("cross-origin POST: %d", r.StatusCode)
	}
	if r := post(c, srv.URL+"/api/runs/..%2f..%2fetc/resume", `{}`, atriumctl); r.StatusCode != 404 {
		t.Fatalf("bad run id: %d", r.StatusCode)
	}
	// Sessions survive a restart (stored hashed), and end on rotation.
	b, _ := os.ReadFile(filepath.Join(auth.dir, "console-sessions.json"))
	if strings.Contains(string(b), ck.Value) {
		t.Fatal("session id stored in clear")
	}
	a2, _, _ := LoadAuth(auth.dir, false)
	req, _ := http.NewRequest("GET", "/", nil)
	req.AddCookie(ck)
	if !a2.Valid(req) {
		t.Fatal("session lost on restart")
	}
	a3, created, _ := LoadAuth(auth.dir, true)
	if !created || a3.Valid(req) || a3.Token() == auth.Token() {
		t.Fatal("rotation did not end the session")
	}
	post(c, srv.URL+"/logout", `{}`, atriumctl)
	if r := get("/api/runs"); r.StatusCode != 401 {
		t.Fatalf("after logout: %d", r.StatusCode)
	}
}

func TestCertificate(t *testing.T) {
	dir := t.TempDir()
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.20")}
	c1, fp1, fresh, err := Certificate(dir, ips)
	if err != nil || !fresh {
		t.Fatal(err, fresh)
	}
	leaf, _ := x509.ParseCertificate(c1.Certificate[0])
	if len(leaf.IPAddresses) != 2 || leaf.NotAfter.Sub(leaf.NotBefore).Hours() > 398*24 {
		t.Fatalf("cert %v %v", leaf.IPAddresses, leaf.NotAfter)
	}
	if st, _ := os.Stat(filepath.Join(dir, "console-key.pem")); st.Mode().Perm() != 0o600 {
		t.Fatal("key file mode")
	}
	_, fp2, fresh, _ := Certificate(dir, ips)
	if fresh || fp2 != fp1 {
		t.Fatal("certificate not reused")
	}
	_, fp3, fresh, _ := Certificate(dir, append(ips, net.ParseIP("10.0.0.5")))
	if !fresh || fp3 == fp1 {
		t.Fatal("new address did not renew the certificate")
	}
}

func TestPrivateAddrsOnly(t *testing.T) {
	addrs, err := PrivateAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range addrs {
		if !ip.IsLoopback() && !ip.IsPrivate() {
			t.Fatalf("public address %v", ip)
		}
	}
	b, _ := json.Marshal(URLs(addrs, 8443))
	if !strings.Contains(string(b), "https://localhost:8443/") {
		t.Fatal(string(b))
	}
}
