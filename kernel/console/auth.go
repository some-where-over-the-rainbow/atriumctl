package console

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	cookieName = "atriumctl_session"
	sessionTTL = 30 * 24 * time.Hour
)

// Auth is the console's token login (decision 49): one token in a file
// only the user can read; signing in with it starts a session held in a
// Secure, HttpOnly, SameSite=Strict cookie. Sessions are stored hashed.
type Auth struct {
	dir   string
	token string

	mu       sync.Mutex
	sessions map[string]time.Time // sha256(session) -> expiry
	failures []time.Time
}

// newToken is 100 random bits as five groups of four base32 characters:
// typable on a phone.
func newToken() string {
	b := make([]byte, 13)
	_, _ = rand.Read(b)
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)[:20]
	return s[:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20]
}

func normalize(t string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(t), "-", ""), " ", ""))
}

// LoadAuth reads the token from dir/console-token, creating it if needed.
// rotate makes a new token and ends every session.
func LoadAuth(dir string, rotate bool) (*Auth, bool, error) {
	a := &Auth{dir: dir, sessions: map[string]time.Time{}}
	p := filepath.Join(dir, "console-token")
	b, err := os.ReadFile(p)
	created := false
	if rotate || errors.Is(err, os.ErrNotExist) || len(strings.TrimSpace(string(b))) == 0 {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, false, err
		}
		b = []byte(newToken())
		if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
			return nil, false, err
		}
		_ = os.Remove(filepath.Join(dir, "console-sessions.json"))
		created = true
	} else if err != nil {
		return nil, false, err
	}
	a.token = strings.TrimSpace(string(b))
	if sb, err := os.ReadFile(filepath.Join(dir, "console-sessions.json")); err == nil {
		_ = json.Unmarshal(sb, &a.sessions)
	}
	return a, created, nil
}

func (a *Auth) Token() string { return a.token }

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (a *Auth) save() {
	b, _ := json.Marshal(a.sessions)
	_ = os.WriteFile(filepath.Join(a.dir, "console-sessions.json"), b, 0o600)
}

// Login checks a token and returns a new session id. Failed attempts are
// limited to five a minute in total.
func (a *Auth) Login(token string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	recent := a.failures[:0]
	for _, t := range a.failures {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	a.failures = recent
	if len(a.failures) >= 5 {
		return "", errTooMany
	}
	if subtle.ConstantTimeCompare([]byte(normalize(token)), []byte(normalize(a.token))) != 1 {
		a.failures = append(a.failures, now)
		return "", errBadToken
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	sid := hex.EncodeToString(b)
	for k, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, k)
		}
	}
	a.sessions[hashOf(sid)] = now.Add(sessionTTL)
	a.save()
	return sid, nil
}

func (a *Auth) Logout(sid string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, hashOf(sid))
	a.save()
}

func (a *Auth) Valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[hashOf(c.Value)]
	return ok && time.Now().Before(exp)
}

var (
	errTooMany  = errors.New("too many failed attempts; wait a minute")
	errBadToken = errors.New("wrong token")
)
