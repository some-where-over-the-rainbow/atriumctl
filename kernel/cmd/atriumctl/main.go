// Command atriumctl is the kernel's command line (slice §4.11).
//
//	atriumctl run     --repo PATH --commit SHA (--task TEXT | --task-file FILE) [--gate manual|auto]
//	            [--models large=opus,small=sonnet] [--host] [--image IMG] [--token-file F] [--needs-bin F]
//	atriumctl resume  RUN [--no-wait]
//	atriumctl status  RUN
//	atriumctl gate    RUN [--approve IDS|all] [--reject IDS] [--no-continue]
//	atriumctl decide  RUN STAGE accept|retry|abort [--no-continue]
//	atriumctl runs
//	atriumctl console [--port 8443] [--rotate-token] [--wasm FILE]
//
// RUN is a run id (under the data directory, --data or $ATRIUMCTL_DATA, default
// ~/.local/share/atriumctl/runs) or a run directory.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"atriumctl/kernel/console"
	"atriumctl/kernel/def"
	"atriumctl/kernel/engine"
	"atriumctl/kernel/qrspi"
	"atriumctl/kernel/run"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	code := 0
	switch os.Args[1] {
	case "run":
		code, err = cmdRun(ctx, os.Args[2:])
	case "resume":
		code, err = cmdResume(ctx, os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "gate":
		code, err = cmdGate(ctx, os.Args[2:])
	case "decide":
		code, err = cmdDecide(ctx, os.Args[2:])
	case "runs":
		err = cmdRuns(os.Args[2:])
	case "console":
		err = cmdConsole(ctx, os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "atriumctl:", err)
		if code == 0 {
			code = 2
		}
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  atriumctl run     --repo PATH --commit SHA (--task TEXT | --task-file FILE) [--gate manual|auto]
              [--models large=opus,small=sonnet] [--host] [--image IMG] [--token-file F] [--needs-bin F]
  atriumctl resume  RUN [--no-wait]
  atriumctl status  RUN
  atriumctl gate    RUN [--approve IDS|all] [--reject IDS] [--no-continue]
  atriumctl decide  RUN STAGE accept|retry|abort [--no-continue]
  atriumctl runs
  atriumctl console [--port 8443] [--rotate-token] [--wasm needs_wasm.wasm]
`)
	os.Exit(2)
}

func dataDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if d := os.Getenv("ATRIUMCTL_DATA"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/share/atriumctl/runs")
}

// parse parses flags that may follow positional arguments.
func parse(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		_ = fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func openRun(data, ref string) (*run.Run, error) {
	dir := ref
	if _, err := os.Stat(filepath.Join(ref, "run.json")); err != nil {
		dir = filepath.Join(dataDir(data), ref)
	}
	return run.Open(dir, qrspi.Slice1())
}

func lookNeeds(flagVal string) (string, error) {
	for _, c := range []string{flagVal, os.Getenv("ATRIUMCTL_NEEDS_BIN")} {
		if c != "" {
			return filepath.Abs(c)
		}
	}
	if p, err := exec.LookPath("needs"); err == nil {
		return p, nil
	}
	if p, err := filepath.Abs("needs/target/release/needs"); err == nil {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("needs binary not found: build it (cargo build --release -p needs in needs/) and pass --needs-bin or $ATRIUMCTL_NEEDS_BIN")
}

func defaultImage() string {
	out, err := exec.Command("claude", "--version").Output()
	if err != nil {
		return ""
	}
	return "atriumctl-harness:" + strings.Fields(string(out))[0]
}

func cmdRun(ctx context.Context, args []string) (int, error) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	data := fs.String("data", "", "data directory")
	repo := fs.String("repo", "", "git repository")
	commit := fs.String("commit", "", "pinned commit")
	task := fs.String("task", "", "task text")
	taskFile := fs.String("task-file", "", "file with the task")
	gate := fs.String("gate", "manual", "gate mode: manual or auto")
	models := fs.String("models", "", "tier overrides, e.g. large=haiku,small=haiku")
	host := fs.Bool("host", false, "run agents as host processes (development)")
	image := fs.String("image", "", "harness image (default atriumctl-harness:<claude version>)")
	home, _ := os.UserHomeDir()
	token := fs.String("token-file", filepath.Join(home, ".config/atriumctl/claude-token"), "long-lived Claude Code token")
	needsBin := fs.String("needs-bin", "", "needs command")
	noWait := fs.Bool("no-wait", false, "do not wait for the usage window; return instead")
	parse(fs, args)
	if *repo == "" || *commit == "" || (*task == "") == (*taskFile == "") {
		return 2, errors.New("run needs --repo, --commit and one of --task or --task-file")
	}
	if *taskFile != "" {
		b, err := os.ReadFile(*taskFile)
		if err != nil {
			return 2, err
		}
		*task = strings.TrimSpace(string(b))
	}
	nb, err := lookNeeds(*needsBin)
	if err != nil {
		return 2, err
	}
	if *image == "" {
		*image = defaultImage()
	}
	repoAbs, _ := filepath.Abs(*repo)
	cfg := run.Config{Repo: repoAbs, Commit: *commit, Task: *task, GateMode: *gate, Image: *image,
		TokenFile: *token, NeedsBin: nb, Host: *host, Models: map[string]string{}}
	for _, kv := range strings.Split(*models, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			cfg.Models[k] = v
		}
	}
	if _, err := os.Stat(cfg.TokenFile); err != nil && !cfg.Host {
		return 2, fmt.Errorf("token file: %w (create one with claude setup-token)", err)
	}
	r, err := run.Create(dataDir(*data), qrspi.Slice1(), cfg)
	if err != nil {
		return 2, err
	}
	defer r.Close()
	fmt.Printf("run %s\n  dir %s\n  repo %s @ %s, %s\n", r.Cfg.ID, r.Dir, r.Cfg.Repo, r.Cfg.Commit[:12], gateLabel(r))
	return drive(ctx, r, *noWait)
}

func gateLabel(r *run.Run) string { return "gate " + r.Cfg.GateMode }

func cmdResume(ctx context.Context, args []string) (int, error) {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	data := fs.String("data", "", "data directory")
	noWait := fs.Bool("no-wait", false, "do not wait for the usage window")
	pos := parse(fs, args)
	if len(pos) != 1 {
		return 2, errors.New("resume needs RUN")
	}
	r, err := openRun(*data, pos[0])
	if err != nil {
		return 2, err
	}
	defer r.Close()
	return drive(ctx, r, *noWait)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	data := fs.String("data", "", "data directory")
	pos := parse(fs, args)
	if len(pos) != 1 {
		return errors.New("status needs RUN")
	}
	r, err := openRun(*data, pos[0])
	if err != nil {
		return err
	}
	defer r.Close()
	st, err := r.Status()
	if err != nil {
		return err
	}
	report(r, st)
	return nil
}

func splitIDs(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func cmdGate(ctx context.Context, args []string) (int, error) {
	fs := flag.NewFlagSet("gate", flag.ExitOnError)
	data := fs.String("data", "", "data directory")
	approve := fs.String("approve", "", "question ids to approve, or all")
	reject := fs.String("reject", "", "question ids to reject")
	noCont := fs.Bool("no-continue", false, "record the answer only")
	pos := parse(fs, args)
	if len(pos) != 1 {
		return 2, errors.New("gate needs RUN")
	}
	r, err := openRun(*data, pos[0])
	if err != nil {
		return 2, err
	}
	defer r.Close()
	prop, err := r.Proposed()
	if err != nil {
		return 2, err
	}
	if *approve == "" && *reject == "" {
		printGate(r, prop)
		return 0, nil
	}
	a := run.GateAnswer{Reject: splitIDs(*reject)}
	if *approve == "all" {
		rej := map[string]bool{}
		for _, id := range a.Reject {
			rej[id] = true
		}
		for _, q := range prop {
			if !rej[q.ID] {
				a.Approve = append(a.Approve, q.ID)
			}
		}
	} else {
		a.Approve = splitIDs(*approve)
	}
	if err := r.Answer(a, "cli"); err != nil {
		return 2, err
	}
	fmt.Printf("approved %v, rejected %v\n", a.Approve, a.Reject)
	if *noCont {
		return 0, nil
	}
	return drive(ctx, r, false)
}

func cmdDecide(ctx context.Context, args []string) (int, error) {
	fs := flag.NewFlagSet("decide", flag.ExitOnError)
	data := fs.String("data", "", "data directory")
	noCont := fs.Bool("no-continue", false, "record the decision only")
	pos := parse(fs, args)
	if len(pos) != 3 {
		return 2, errors.New("decide needs RUN STAGE accept|retry|abort")
	}
	r, err := openRun(*data, pos[0])
	if err != nil {
		return 2, err
	}
	defer r.Close()
	if err := r.Decide(pos[1], engine.Decision(pos[2])); err != nil {
		return 2, err
	}
	if *noCont {
		return 0, nil
	}
	return drive(ctx, r, false)
}

func cmdRuns(args []string) error {
	fs := flag.NewFlagSet("runs", flag.ExitOnError)
	data := fs.String("data", "", "data directory")
	parse(fs, args)
	ents, err := os.ReadDir(dataDir(*data))
	if err != nil {
		return err
	}
	var names []string
	for _, e := range ents {
		if _, err := os.Stat(filepath.Join(dataDir(*data), e.Name(), "run.json")); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		var c run.Config
		b, _ := os.ReadFile(filepath.Join(dataDir(*data), n, "run.json"))
		_ = json.Unmarshal(b, &c)
		task := c.Task
		if len(task) > 60 {
			task = task[:57] + "..."
		}
		fmt.Printf("%s  %s  %q\n", n, filepath.Base(c.Repo), task)
	}
	return nil
}

// drive takes the run's lock, starts its services and runs it until it
// needs the architect.
func drive(ctx context.Context, r *run.Run, noWait bool) (int, error) {
	if err := r.Lock(); err != nil {
		return 2, err
	}
	if err := r.Start(); err != nil {
		return 2, err
	}
	st, err := r.Drive(ctx, run.DriveOptions{
		NoWait: noWait,
		OnAuto: func(ids []string) { fmt.Printf("gate (auto): approving %v\n", ids) },
		OnWait: func(until time.Time) {
			fmt.Printf("waiting %s for the usage window (until %s); Ctrl-C to stop, `atriumctl resume %s` later\n",
				time.Until(until).Round(time.Second), until.Local().Format("15:04"), r.Cfg.ID)
		},
	})
	report(r, st)
	if err != nil {
		return 1, err
	}
	if st.State == engine.Failed {
		return 1, nil
	}
	return 0, nil
}

func report(r *run.Run, st engine.Status) {
	fmt.Printf("run %s: %s\n", r.Cfg.ID, st.State)
	for _, g := range st.Gates {
		prop, _ := r.Proposed()
		fmt.Printf("  gate %q (attempt %d) waits for the architect\n", g.Node, g.Attempt)
		printGate(r, prop)
	}
	for _, x := range st.Exhausted {
		fmt.Printf("  stage %s exhausted (attempt %d): %s\n", x.Node, x.Attempt, firstLines(x.Reason, 6))
		fmt.Printf("    decide: atriumctl decide %s %s accept|retry|abort\n", r.Cfg.ID, x.Node)
	}
	for _, w := range st.Waits {
		fmt.Printf("  stage %s waits until %s: %s\n", w.Node, w.Until.Local().Format(time.RFC3339), w.Reason)
	}
	for _, f := range st.Failed {
		fmt.Printf("  stage %s failed: %s\n    retry: atriumctl resume %s\n", f.Node, firstLines(f.Err, 4), r.Cfg.ID)
	}
	if st.State == engine.Done {
		fmt.Printf("  artifacts: %s\n  trace: %s\n", filepath.Join(r.Dir, "artifacts"), filepath.Join(r.Dir, "trace.jsonl"))
	}
}

func printGate(r *run.Run, prop []run.Question) {
	if len(prop) == 0 {
		fmt.Println("  no proposed questions")
		return
	}
	for _, q := range prop {
		fmt.Printf("    %s  %s\n", q.ID, q.Title)
	}
	fmt.Printf("  answer: atriumctl gate %s --approve Q_001,Q_002|all [--reject Q_003]\n", r.Cfg.ID)
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}
	return strings.Join(lines, "\n      ")
}

func cmdConsole(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("console", flag.ExitOnError)
	data := fs.String("data", "", "data directory")
	port := fs.Int("port", 8443, "HTTPS port")
	home, _ := os.UserHomeDir()
	cfgDir := fs.String("config-dir", filepath.Join(home, ".config/atriumctl"), "token, sessions and certificate")
	rotate := fs.Bool("rotate-token", false, "make a new token and end all sessions")
	wasm := fs.String("wasm", "", "needs_wasm.wasm (default: next to the needs build)")
	parse(fs, args)
	if *wasm == "" {
		if nb, err := lookNeeds(""); err == nil {
			*wasm = filepath.Join(filepath.Dir(nb), "..", "wasm32-unknown-unknown", "release", "needs_wasm.wasm")
		}
	}
	if _, err := os.Stat(*wasm); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s not found; build it with cargo build --release -p needs-wasm --target wasm32-unknown-unknown\n", *wasm)
		*wasm = ""
	}
	auth, created, err := console.LoadAuth(*cfgDir, *rotate)
	if err != nil {
		return err
	}
	addrs, err := console.PrivateAddrs()
	if err != nil {
		return err
	}
	cert, fp, fresh, err := console.Certificate(*cfgDir, addrs)
	if err != nil {
		return err
	}
	srv := &console.Server{DataDir: dataDir(*data), Def: func() *def.Swarm { return qrspi.Slice1() }, WasmPath: *wasm, Auth: auth,
		Log: func(f string, a ...any) { fmt.Printf(time.Now().Format("15:04:05 ")+f+"\n", a...) }}
	fmt.Println("architect console")
	for _, u := range console.URLs(addrs, *port) {
		fmt.Println("  " + u)
	}
	note := ""
	if fresh {
		note = " (new)"
	}
	fmt.Printf("certificate%s SHA-256 %s\n", note, fp)
	if created {
		fmt.Printf("new token written to %s\n", filepath.Join(*cfgDir, "console-token"))
	} else {
		fmt.Printf("token in %s\n", filepath.Join(*cfgDir, "console-token"))
	}
	return srv.Serve(ctx, cert, addrs, *port)
}
