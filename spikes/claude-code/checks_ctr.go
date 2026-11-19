package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// inCtr runs a check with agents in harness containers.
func inCtr(fn func(*Env) *CheckResult) func(*Env) *CheckResult {
	return func(e *Env) *CheckResult {
		if _, err := os.Stat(e.ctrCfg.TokenFile); err != nil {
			return &CheckResult{Status: Fail, Notes: []string{"no token file: " + err.Error()}}
		}
		e.Ctr = e.ctrCfg
		defer func() { e.Ctr = nil }()
		r := fn(e)
		for _, run := range r.Runs {
			if run.ContainerLeft {
				r.Status = Fail
				r.notef("container %s still exists after the run", run.ContainerName)
			}
			if run.Result == nil && !run.Aborted {
				r.notef("%s: exit=%s stderr=%s", run.ID, run.ExitErr, run.Stderr)
			}
		}
		return r
	}
}

func checkCtrProbe(e *Env) *CheckResult {
	c := &CheckResult{}
	out, err := exec.Command("podman", "run", "--rm", "--network", "none", e.ctrCfg.Image, "probe").CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		c.notef("%s", l)
	}
	c.Status = map[bool]Status{true: Pass, false: Fail}[err == nil]
	return c
}

func checkCtrAbort(e *Env) *CheckResult {
	c := checkAbort(e)
	for _, r := range c.Runs {
		c.notef("container %s exists after abort: %v", r.ContainerName, r.ContainerLeft)
	}
	return c
}

func checkCtrResources(e *Env) *CheckResult {
	c := &CheckResult{Status: Pass}
	one := func(id string) *AgentRun {
		return RunAgent(context.Background(), e.agent(id, "Reply with the single word: ok"))
	}
	r := one("cres-1")
	c.Runs = append(c.Runs, r)
	c.notef("single: init %s, result %s, exit %s, container cgroup peak %d MB", ms(r.ToInit), ms(r.ToResult), ms(r.ToExit), r.CgroupPeakKB/1024)
	var wg sync.WaitGroup
	rs := make([]*AgentRun, 3)
	t := time.Now()
	for i := range rs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); rs[i] = one(fmt.Sprintf("cres-p%d", i)) }(i)
	}
	wg.Wait()
	var sum int64
	for i, r := range rs {
		c.Runs = append(c.Runs, r)
		sum += r.CgroupPeakKB
		c.notef("parallel %d: init %s, result %s, cgroup peak %d MB", i, ms(r.ToInit), ms(r.ToResult), r.CgroupPeakKB/1024)
		if r.Result == nil {
			c.Status = Fail
		}
	}
	c.notef("three in parallel: wall %s, sum of peaks %d MB", ms(time.Since(t)), sum/1024)
	return c
}

func checkCtrEgress(e *Env) *CheckResult {
	c := &CheckResult{}
	type agg struct{ allowed, denied int }
	by := map[string]*agg{}
	for _, l := range e.Proxy.Log() {
		k := l.Method + " " + l.Target
		if by[k] == nil {
			by[k] = &agg{}
		}
		if l.Allowed {
			by[k].allowed++
		} else {
			by[k].denied++
		}
	}
	for k, v := range by {
		c.notef("%s: allowed %d, denied %d", k, v.allowed, v.denied)
	}
	c.Status = Pass
	if len(by) == 0 {
		c.Status = Fail
		c.notef("no egress at all; container checks did not run?")
	}
	for k, v := range by {
		if v.allowed > 0 && k != "CONNECT api.anthropic.com:443" {
			c.Status = Fail
		}
	}
	return c
}

// The token must not appear in anything we keep: stream events, stderr,
// gateway calls, telemetry, egress log, the work tree, the results so far.
func checkCtrToken(e *Env) *CheckResult {
	c := &CheckResult{}
	b, err := os.ReadFile(e.ctrCfg.TokenFile)
	tok := strings.TrimSpace(string(b))
	if err != nil || len(tok) < 20 {
		c.Status = Fail
		c.notef("no usable token file")
		return c
	}
	hits := map[string]bool{}
	scan := func(what string, v any) {
		j, _ := json.Marshal(v)
		if strings.Contains(string(j), tok) {
			hits[what] = true
		}
	}
	for _, r := range *e.results {
		for _, run := range r.Runs {
			scan("events of "+run.ID, run.Events)
			scan("stderr of "+run.ID, run.Stderr)
			scan("run record of "+run.ID, run)
		}
	}
	scan("gateway calls", e.GW.Calls(""))
	scan("telemetry", e.OTel.Records())
	scan("egress log", e.Proxy.Log())
	_ = filepath.WalkDir(e.Tree, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), tok) {
				hits["work tree "+p] = true
			}
		}
		return nil
	})
	c.notef("token (%d chars) searched in all runs' events, stderr and records, gateway calls, telemetry, egress log, work tree", len(tok))
	if len(hits) > 0 {
		c.Status = Fail
		for h := range hits {
			c.notef("FOUND in %s", h)
		}
	} else {
		c.Status = Pass
		c.notef("not found anywhere")
	}
	return c
}
