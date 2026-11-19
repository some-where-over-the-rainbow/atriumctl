// Package run is a run of a swarm definition: its directory, services
// (tool gateway, egress proxy, telemetry receiver, executor) and the
// compiled engine graph.
//
// A run directory holds:
//
//	run.json        configuration, written once
//	journal.jsonl   the engine's state
//	trace.jsonl     trace events (slice §13)
//	repo/           read-only snapshot of the repository at the pinned commit
//	artifacts/      task.md, questions.md, research.md, needs.toml
//	needs/          needs.json snapshot per stage attempt
//	s/              kernel.sock and proxy.sock for the harness containers
//	claude-config/  empty, mounted read-only as CLAUDE_CONFIG_DIR
package run

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"atriumctl/kernel/def"
	"atriumctl/kernel/egress"
	"atriumctl/kernel/engine"
	"atriumctl/kernel/executor"
	"atriumctl/kernel/gateway"
	"atriumctl/kernel/telemetry"
	"atriumctl/kernel/trace"
)

type Config struct {
	ID        string            `json:"id"`
	Repo      string            `json:"repo"`   // path of the git repository
	Commit    string            `json:"commit"` // pinned commit
	Task      string            `json:"task"`
	GateMode  string            `json:"gate_mode"` // manual or auto
	Image     string            `json:"image"`     // harness image
	TokenFile string            `json:"token_file"`
	NeedsBin  string            `json:"needs_bin"`
	Models    map[string]string `json:"models,omitempty"` // tier overrides
	Host      bool              `json:"host,omitempty"`   // agents as host processes (development)
	Created   time.Time         `json:"created"`
	DefHash   string            `json:"definition_hash"`
}

type Run struct {
	Cfg Config
	Dir string
	Def *def.Swarm

	eng       *engine.Engine
	gw        *gateway.Gateway
	x         *executor.Executor
	sink      *sink
	repoFiles map[string]int

	servers []*http.Server
	started bool
	unlock  func()
	seqMu   sync.Mutex
	seq     map[string]int
}

func newID() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-1504") + "-" + hex.EncodeToString(b)
}

// Create makes a new run directory under dataDir and snapshots the repository.
func Create(dataDir string, d *def.Swarm, cfg Config) (*Run, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if cfg.ID == "" {
		cfg.ID = newID()
	}
	if cfg.GateMode == "" {
		cfg.GateMode = "manual"
	}
	cfg.Created = time.Now().UTC()
	cfg.DefHash = d.Hash()
	dir := filepath.Join(dataDir, cfg.ID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	for _, sub := range []string{"repo", "artifacts", "needs", "s", "claude-config"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	commit, err := snapshot(cfg.Repo, cfg.Commit, filepath.Join(dir, "repo"))
	if err != nil {
		return nil, fmt.Errorf("snapshot of %s at %s: %w", cfg.Repo, cfg.Commit, err)
	}
	cfg.Commit = commit
	for _, a := range d.Artifacts {
		if err := os.WriteFile(filepath.Join(dir, "artifacts", a), nil, 0o600); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "artifacts", "needs.toml"), []byte(d.NeedsConfig), 0o400); err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "run.json"), append(b, '\n'), 0o600); err != nil {
		return nil, err
	}
	return Open(dir, d)
}

// Open loads an existing run.
func Open(dir string, d *def.Swarm) (*Run, error) {
	b, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if cfg.DefHash != d.Hash() {
		return nil, fmt.Errorf("run %s was created with definition %s; this kernel has %s", cfg.ID, cfg.DefHash, d.Hash())
	}
	r := &Run{Cfg: cfg, Dir: dir, Def: d, seq: map[string]int{}}
	if r.repoFiles, err = countLines(filepath.Join(dir, "repo")); err != nil {
		return nil, err
	}
	if err := r.prepare(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Run) path(p ...string) string { return filepath.Join(append([]string{r.Dir}, p...)...) }

// prepare opens the trace and the journal and compiles the graph: enough
// to read the run's state, answer gates and record decisions.
func (r *Run) prepare() error {
	f, err := trace.OpenFile(r.path("trace.jsonl"))
	if err != nil {
		return err
	}
	r.sink = &sink{file: f, writes: map[string][]string{}}
	g, err := r.compile()
	if err != nil {
		return err
	}
	r.eng, err = engine.New(g, &engine.FileJournal{Path: r.path("journal.jsonl")})
	if err != nil {
		return err
	}
	r.eng.MaxParallel = r.Def.MaxParallel
	r.eng.Hook = r.engineHook
	return nil
}

// Start starts the services agents need: tool gateway, telemetry receiver
// and egress proxy on the run's sockets, and the executor. Only the process
// holding the run's lock may start them.
func (r *Run) Start() error {
	if r.started {
		return nil
	}
	r.gw = gateway.New(r.sink)
	r.gw.NeedsBin = r.Cfg.NeedsBin
	r.gw.RepoFiles = func() map[string]int { return r.repoFiles }

	mux := http.NewServeMux()
	mux.Handle("/mcp/", r.gw)
	mux.Handle("/v1/", &telemetry.Receiver{Trace: r.sink})
	if err := r.serve("unix", r.path("s", "kernel.sock"), mux); err != nil {
		return err
	}
	if err := r.serve("unix", r.path("s", "proxy.sock"), &egress.Proxy{Allow: egress.Anthropic, Trace: r.sink}); err != nil {
		return err
	}
	r.x = &executor.Executor{Trace: r.sink}
	if r.Cfg.Host {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		r.listen(ln, mux)
		r.x.Runner = &executor.HostRunner{BaseURL: "http://" + ln.Addr().String(), TokenFile: r.Cfg.TokenFile, Dir: r.path("claude-config")}
	} else {
		r.x.Runner = &executor.ContainerRunner{Image: r.Cfg.Image, RunDir: r.path("s"), TokenFile: r.Cfg.TokenFile, ConfigDir: r.path("claude-config")}
	}
	r.started = true
	return nil
}

func (r *Run) serve(network, addr string, h http.Handler) error {
	_ = os.Remove(addr)
	ln, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	r.listen(ln, h)
	return nil
}

func (r *Run) listen(ln net.Listener, h http.Handler) {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	r.servers = append(r.servers, srv)
	go func() { _ = srv.Serve(ln) }()
}

func (r *Run) Close() {
	for _, s := range r.servers {
		_ = s.Close()
	}
	r.servers, r.started = nil, false
	if r.unlock != nil {
		r.unlock()
		r.unlock = nil
	}
	if r.eng != nil {
		if fj, ok := r.eng.J.(*engine.FileJournal); ok {
			_ = fj.Close()
		}
	}
	if r.sink != nil {
		_ = r.sink.file.Close()
	}
}

func (r *Run) emit(typ, stage string, attempt int, f map[string]any) {
	r.sink.Emit(trace.Event{Type: typ, SchemaVersion: trace.SchemaVersion, Source: trace.SourceKernel,
		TsStart: time.Now(), RunID: r.Cfg.ID, Stage: stage, StageAttempt: attempt, Fields: f})
}

func (r *Run) engineHook(ev engine.Event) {
	f := map[string]any{"event": ev.Kind}
	if ev.Item >= 0 {
		f["item"] = ev.Item
	}
	if ev.Err != nil {
		f["error"] = ev.Err.Error()
	}
	r.emit("stage", ev.Node, ev.Attempt, f)
}

// Step runs the graph until it cannot go on (engine.Run).
func (r *Run) Step(ctx context.Context) (engine.Status, error) {
	r.emit("run", "", 0, map[string]any{"event": "step", "repo_commit": r.Cfg.Commit, "repo_files": len(r.repoFiles),
		"executor": "claude-code", "gate_mode": r.Cfg.GateMode, "definition_hash": r.Cfg.DefHash})
	st, err := r.eng.Run(ctx, r.Cfg.Task)
	r.emit("run", "", 0, map[string]any{"event": "pause", "state": string(st.State)})
	return st, err
}

// RepoFiles lists the snapshot's files with line counts.
func (r *Run) RepoFiles() map[string]int { return r.repoFiles }

// Stages lists the graph's stages in order, with their kinds.
func (r *Run) Stages() []def.Stage { return r.Def.Stages }

// Status reads the run's state without running anything.
func (r *Run) Status() (engine.Status, error) { return r.eng.Status() }

// Decide records the architect's decision on an exhausted stage.
func (r *Run) Decide(stage string, d engine.Decision) error {
	if err := r.Lock(); err != nil {
		return err
	}
	r.emit("decision", stage, 0, map[string]any{"decision": string(d), "channel": "cli"})
	return r.eng.Decide(stage, d)
}

// sink writes the trace file and remembers which agent wrote which artifact.
type sink struct {
	file   *trace.File
	mu     sync.Mutex
	writes map[string][]string // artifact path -> agent ids
}

func (s *sink) Emit(e trace.Event) {
	if e.Type == "artifact" {
		if p, ok := e.Fields["path"].(string); ok {
			s.mu.Lock()
			s.writes[p] = append(s.writes[p], e.AgentID)
			s.mu.Unlock()
		}
	}
	s.file.Emit(e)
}

func (s *sink) writtenBy(path string, agents map[string]bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.writes[path] {
		if agents[a] {
			return true
		}
	}
	return false
}

// snapshot extracts the repository at commit into dst (git archive) and
// makes it read-only. It returns the full commit id.
func snapshot(repo, commit, dst string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", commit+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("unknown commit: %w", err)
	}
	full := strings.TrimSpace(string(out))
	cmd := exec.Command("git", "-C", repo, "archive", "--format=tar", full)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	tr := tar.NewReader(pipe)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		name := filepath.Clean(h.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return "", fmt.Errorf("unsafe path in archive: %s", h.Name)
		}
		target := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return "", err
			}
			f.Close()
		case tar.TypeSymlink:
			if err := os.Symlink(h.Linkname, target); err != nil {
				return "", err
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("git archive: %v: %s", err, stderr.String())
	}
	// Read-only: the gateway mounts it read-only too; this is a second layer.
	_ = filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink == 0 {
			if d.IsDir() {
				_ = os.Chmod(p, 0o555)
			} else {
				_ = os.Chmod(p, 0o444)
			}
		}
		return nil
	})
	return full, nil
}

// countLines lists the snapshot's files with their line counts, for
// evidence checks.
func countLines(root string) (map[string]int, error) {
	out := map[string]int{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n := bytes.Count(b, []byte{'\n'})
		if len(b) > 0 && b[len(b)-1] != '\n' {
			n++
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = n
		return nil
	})
	return out, err
}

// ErrLocked: another process drives the run.
var ErrLocked = errors.New("run is driven by another process")

// Lock makes this process the run's only driver until Close. Answers and
// decisions also take the lock, so they never race a driving process.
func (r *Run) Lock() error {
	if r.unlock != nil {
		return nil
	}
	f, err := os.OpenFile(r.path("run.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid, _ := os.ReadFile(r.path("run.lock"))
		f.Close()
		return fmt.Errorf("%w (pid %s)", ErrLocked, strings.TrimSpace(string(pid)))
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
	r.unlock = func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
	return nil
}

// Driver reports the pid of the process driving the run, or 0.
func (r *Run) Driver() int {
	if r.unlock != nil {
		return os.Getpid()
	}
	f, err := os.OpenFile(r.path("run.lock"), os.O_RDWR, 0)
	if err != nil {
		return 0
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return 0
	}
	b, _ := os.ReadFile(r.path("run.lock"))
	var pid int
	_, _ = fmt.Sscan(string(b), &pid)
	return pid
}
