// Command check runs the engine spike's checks (slice §6.3) against a
// candidate binary that implements the contract in package common.
//
//	go run ./check -bin bin/adk [-pkg ./adk]
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"atriumctl/spikes/engine/common"
)

type result struct {
	id, status string
	notes      []string
}

var (
	bin = flag.String("bin", "", "candidate binary")
	pkg = flag.String("pkg", "", "candidate package, for the size check")
	tmp = flag.String("tmp", os.TempDir(), "scratch parent dir")
)

func main() {
	flag.Parse()
	if abs, err := filepath.Abs(*bin); err == nil {
		*bin = abs
	}
	var rs []result
	for _, c := range []struct {
		id string
		fn func(dir string) result
	}{
		{"fanout", checkFanout},
		{"resume-after-kill", checkResumeAfterKill},
		{"timeout", checkTimeout},
		{"observe", checkObserve},
		{"crash-mid-fanout", checkCrashMid},
		{"size", checkSize},
	} {
		dir, _ := os.MkdirTemp(*tmp, "eng-"+c.id+"-")
		r := c.fn(dir)
		r.id = c.id
		rs = append(rs, r)
		fmt.Printf("== %-18s %s\n", r.id, r.status)
		for _, n := range r.notes {
			fmt.Println("   -", n)
		}
		_ = os.RemoveAll(dir)
	}
}

func run(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command(*bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func paths(dir string) (db, log string) {
	return filepath.Join(dir, "s.db"), filepath.Join(dir, "l.jsonl")
}

func killedBySignal(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return ws.Signal() == syscall.SIGKILL
		}
	}
	return false
}

func count(rs []common.Rec, ev, node string) int {
	n := 0
	for _, r := range rs {
		if r.Ev == ev && r.Node == node {
			n++
		}
	}
	return n
}

func status(ok bool) string { return map[bool]string{true: "PASS", false: "FAIL"}[ok] }

// Width decided at run time (the architect approves 5 of 9), cap 3, join waits for all.
func checkFanout(dir string) result {
	var r result
	db, lg := paths(dir)
	if out, err := run(dir, nil, "run", "-db", db, "-log", lg, "-width", "9"); err != nil {
		return result{status: "FAIL", notes: []string{"run: " + err.Error() + " " + out}}
	}
	approve := "Q_002,Q_004,Q_005,Q_007,Q_009"
	if out, err := run(dir, nil, "resume", "-db", db, "-log", lg, "-width", "9", "-approve", approve); err != nil {
		return result{status: "FAIL", notes: []string{"resume: " + err.Error() + " " + out}}
	}
	rs := common.ReadLog(lg)
	var items []string
	var maxConc int64
	var lastWorkerEnd, synthStart int64
	result := ""
	for _, x := range rs {
		switch {
		case x.Ev == "start" && x.Node == "worker":
			items = append(items, x.Item)
		case x.Ev == "conc" && x.Conc > maxConc:
			maxConc = x.Conc
		case x.Ev == "end" && x.Node == "worker" && x.T > lastWorkerEnd:
			lastWorkerEnd = x.T
		case x.Ev == "start" && x.Node == "synthesize":
			synthStart = x.T
		case x.Ev == "result":
			result = fmt.Sprint(x.Data)
		}
	}
	sort.Strings(items)
	r.notes = append(r.notes,
		fmt.Sprintf("workers started for %v", items),
		fmt.Sprintf("max concurrent workers: %d (cap 3)", maxConc),
		fmt.Sprintf("synthesize started %.0f ms after the last worker ended", float64(synthStart-lastWorkerEnd)/1e6),
		fmt.Sprintf("result: %s", result))
	ok := strings.Join(items, ",") == approve && maxConc == 3 && synthStart > lastWorkerEnd && strings.HasPrefix(result, "5 facts")
	r.status = status(ok)
	return r
}

// The process dies (SIGKILL) at the gate; a new process resumes from the file.
func checkResumeAfterKill(dir string) result {
	var r result
	db, lg := paths(dir)
	_, err := run(dir, nil, "run", "-db", db, "-log", lg, "-width", "4", "-crash")
	r.notes = append(r.notes, fmt.Sprintf("first process killed by SIGKILL at the pause: %v", killedBySignal(err)))
	out, err := run(dir, nil, "resume", "-db", db, "-log", lg, "-width", "4", "-approve", "Q_001,Q_003")
	if err != nil {
		r.notes = append(r.notes, "resume: "+err.Error()+" "+out)
	}
	rs := common.ReadLog(lg)
	pids := map[int]bool{}
	for _, x := range rs {
		pids[x.PID] = true
	}
	done := count(rs, "done", "")
	r.notes = append(r.notes,
		fmt.Sprintf("processes: %d; question ran %d time(s); gate started %d time(s); workers %d; done %d",
			len(pids), count(rs, "start", "question"), count(rs, "start", "gate"), count(rs, "start", "worker"), done))
	r.status = status(err == nil && len(pids) == 2 && count(rs, "start", "question") == 1 && count(rs, "start", "worker") == 2 && done == 1)
	return r
}

// A node with a 1 s timeout starts `sleep 30`; the node fails on time and the child is gone.
func checkTimeout(dir string) result {
	var r result
	_, lg := paths(dir)
	t := time.Now()
	out, err := run(dir, nil, "timeout", "-log", lg)
	wall := time.Since(t)
	rs := common.ReadLog(lg)
	var child int
	var start, end int64
	var nodeErr string
	for _, x := range rs {
		switch {
		case x.Ev == "child":
			child = x.Child
		case x.Ev == "start" && x.Node == "slow":
			start = x.T
		case x.Ev == "end" && x.Node == "slow":
			end, nodeErr = x.T, x.Err
		}
	}
	alive := child > 0 && syscall.Kill(child, 0) == nil
	r.notes = append(r.notes,
		fmt.Sprintf("node ended %.0f ms after start with error %q", float64(end-start)/1e6, nodeErr),
		fmt.Sprintf("child pid %d still alive: %v", child, alive),
		fmt.Sprintf("process exit: %v (%s); wall %s", err, firstLine(out), wall.Round(time.Millisecond)))
	d := time.Duration(end - start)
	r.status = status(end > 0 && d > 900*time.Millisecond && d < 3500*time.Millisecond && strings.Contains(nodeErr, "deadline") && !alive)
	return r
}

// Every node start has an end, from both processes, through the wrapper hook.
func checkObserve(dir string) result {
	var r result
	db, lg := paths(dir)
	_, _ = run(dir, nil, "run", "-db", db, "-log", lg, "-width", "3")
	_, _ = run(dir, nil, "resume", "-db", db, "-log", lg, "-width", "3", "-approve", "Q_001,Q_002,Q_003")
	rs := common.ReadLog(lg)
	open := map[string]int{}
	nodes := map[string]bool{}
	for _, x := range rs {
		k := fmt.Sprintf("%d/%s/%s", x.PID, x.Node, x.Item)
		switch x.Ev {
		case "start":
			open[k]++
			nodes[x.Node] = true
		case "end":
			open[k]--
		}
	}
	var unbalanced []string
	for k, v := range open {
		if v != 0 {
			unbalanced = append(unbalanced, k)
		}
	}
	var ns []string
	for n := range nodes {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	r.notes = append(r.notes, fmt.Sprintf("nodes observed: %v; unbalanced start/end: %v", ns, unbalanced))
	r.status = status(len(unbalanced) == 0 && len(ns) == 4)
	return r
}

// Beyond §6.3: the process dies when worker 5 of 6 starts (at least 2 done). Can the run
// continue, and which workers run again?
func checkCrashMid(dir string) result {
	var r result
	db, lg := paths(dir)
	_, _ = run(dir, nil, "run", "-db", db, "-log", lg, "-width", "6")
	_, err := run(dir, []string{"SPIKE_DIE_AT_WORKER=5"}, "resume", "-db", db, "-log", lg, "-width", "6", "-approve", "Q_001,Q_002,Q_003,Q_004,Q_005,Q_006")
	r.notes = append(r.notes, fmt.Sprintf("killed mid-fan-out: %v", killedBySignal(err)))
	out, err := run(dir, nil, "continue", "-db", db, "-log", lg, "-width", "6", "-approve", "Q_001,Q_002,Q_003,Q_004,Q_005,Q_006")
	r.notes = append(r.notes, fmt.Sprintf("continue: err=%v out=%q", err, firstLine(out)))
	rs := common.ReadLog(lg)
	byPID := map[int][]string{}
	for _, x := range rs {
		if x.Ev == "start" && x.Node == "worker" {
			byPID[x.PID] = append(byPID[x.PID], x.Item)
		}
	}
	for p, items := range byPID {
		sort.Strings(items)
		r.notes = append(r.notes, fmt.Sprintf("pid %d started workers %v", p, items))
	}
	r.notes = append(r.notes, fmt.Sprintf("done: %d", count(rs, "done", "")))
	switch {
	case count(rs, "done", "") == 1:
		r.status = "PASS"
	default:
		r.status = "FAIL"
	}
	return r
}

func checkSize(dir string) result {
	var r result
	if *pkg == "" {
		return result{status: "SKIP"}
	}
	out := filepath.Join(dir, "bin")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, *pkg)
	if b, err := cmd.CombinedOutput(); err != nil {
		return result{status: "FAIL", notes: []string{string(b)}}
	}
	st, _ := os.Stat(out)
	mods, _ := exec.Command("go", "version", "-m", out).Output()
	n := strings.Count(string(mods), "\tdep\t")
	r.notes = append(r.notes, fmt.Sprintf("stripped binary %.1f MB; %d modules linked", float64(st.Size())/1e6, n))
	r.status = "INFO"
	return r
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
