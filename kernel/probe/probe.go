// Package probe runs declared test commands in a work sandbox, initiated by
// the kernel only (slice §4.7, console §4): test and coverage figures come
// from running the tests, never from an agent's account of them.
//
// The probe image is the declared base plus the repository's test
// dependencies, built once per content hash (network only at build time).
// A run copies the repository snapshot, runs the command in a container
// without network, and parses a JUnit report and coverage.py's JSON.
package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Spec is a probe as the definition declares it.
type Spec struct {
	Base         string        // base image, e.g. docker.io/library/python:3.12-slim
	Requirements []string      // repository files installed with pip -r, if present
	Install      []string      // extra pip packages, e.g. pytest, pytest-cov
	Command      []string      // run in /work; reports go to /out
	Omit         []string      // coverage omit patterns, e.g. tests/*
	Timeout      time.Duration // wall time of the test run
}

// Pytest is the slice-1 baseline: pytest with JUnit and JSON coverage,
// lines and branches, without the tests themselves.
func Pytest() Spec {
	return Spec{
		Base:         "docker.io/library/python:3.12-slim",
		Requirements: []string{"requirements.txt", "requirements-dev.txt"},
		Install:      []string{"pytest", "pytest-cov"},
		Command: []string{"python", "-m", "pytest", "-q", "-p", "no:cacheprovider",
			"--junitxml=/out/junit.xml", "--cov=.", "--cov-branch", "--cov-config=/probe/coveragerc",
			"--cov-report=json:/out/coverage.json"},
		Omit:    []string{"tests/*", "*/tests/*", "test_*.py", "*/test_*.py", "conftest.py"},
		Timeout: 10 * time.Minute,
	}
}

type Failure struct {
	Test    string `json:"test"`
	Kind    string `json:"kind"` // failure or error
	Message string `json:"message"`
}

type Coverage struct {
	Covered int     `json:"covered"`
	Total   int     `json:"total"`
	Percent float64 `json:"percent"`
}

type FileCoverage struct {
	Path    string   `json:"path"`
	Lines   Coverage `json:"lines"`
	Branch  Coverage `json:"branches"`
	Missing []int    `json:"missing_lines,omitempty"`
}

type Result struct {
	Probe      string         `json:"probe"`
	Image      string         `json:"image"`
	Command    []string       `json:"command"`
	Started    time.Time      `json:"started"`
	DurationMS int64          `json:"duration_ms"`
	ExitCode   int            `json:"exit_code"`
	Tests      int            `json:"tests"`
	Passed     int            `json:"passed"`
	Failed     int            `json:"failed"`
	Errors     int            `json:"errors"`
	Skipped    int            `json:"skipped"`
	Failures   []Failure      `json:"failures,omitempty"`
	Lines      Coverage       `json:"lines"`
	Branches   Coverage       `json:"branches"`
	Files      []FileCoverage `json:"files,omitempty"`
	Error      string         `json:"error,omitempty"` // the probe could not run or report
	Output     string         `json:"output,omitempty"`
}

// Runner builds probe images and runs probes with Podman.
type Runner struct {
	Podman string // default "podman"
	Log    io.Writer
}

func (r *Runner) podman() string {
	if r.Podman == "" {
		return "podman"
	}
	return r.Podman
}

// ImageTag is atriumctl-probe:<hash of base, requirement files and packages>.
func ImageTag(spec Spec, repo string) (string, map[string][]byte, error) {
	h := sha256.New()
	fmt.Fprintf(h, "base %s\ninstall %s\n", spec.Base, strings.Join(spec.Install, " "))
	reqs := map[string][]byte{}
	for _, f := range spec.Requirements {
		b, err := os.ReadFile(filepath.Join(repo, f))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		reqs[f] = b
		fmt.Fprintf(h, "file %s %d\n", f, len(b))
		h.Write(b)
	}
	return "atriumctl-probe:" + hex.EncodeToString(h.Sum(nil))[:12], reqs, nil
}

// EnsureImage builds the probe image for the repository unless it exists.
func (r *Runner) EnsureImage(ctx context.Context, spec Spec, repo, scratch string) (string, error) {
	tag, reqs, err := ImageTag(spec, repo)
	if err != nil {
		return "", err
	}
	if exec.CommandContext(ctx, r.podman(), "image", "exists", tag).Run() == nil {
		return tag, nil
	}
	dir, err := os.MkdirTemp(scratch, "probe-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := os.Mkdir(filepath.Join(dir, "reqs"), 0o755); err != nil {
		return "", err
	}
	var pip []string
	names := make([]string, 0, len(reqs))
	for n := range reqs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		// Requirement files may refer to each other (-r requirements.txt): keep names.
		if err := os.WriteFile(filepath.Join(dir, "reqs", filepath.Base(n)), reqs[n], 0o644); err != nil {
			return "", err
		}
		pip = append(pip, "-r", "/reqs/"+filepath.Base(n))
	}
	pip = append(pip, spec.Install...)
	cf := fmt.Sprintf("FROM %s\nCOPY reqs/ /reqs/\nRUN pip install --no-cache-dir --disable-pip-version-check %s\nWORKDIR /work\n",
		spec.Base, shellJoin(pip))
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(cf), 0o644); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, r.podman(), "build", "-t", tag, "-f", filepath.Join(dir, "Containerfile"), dir)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if r.Log != nil {
		cmd.Stdout = io.MultiWriter(&out, r.Log)
		cmd.Stderr = cmd.Stdout
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("building %s: %v\n%s", tag, err, tail(out.String(), 3000))
	}
	return tag, nil
}

func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// Run copies the repository into scratch, runs the probe in a container
// without network and parses its reports. Errors that prevent a result are
// returned in Result.Error, so a broken probe never fails a run.
func (r *Runner) Run(ctx context.Context, name string, spec Spec, image, repo, scratch string) *Result {
	res := &Result{Probe: name, Image: image, Command: spec.Command, Started: time.Now().UTC()}
	work, err := os.MkdirTemp(scratch, "probe-run-")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer os.RemoveAll(work)
	src, out, cfg := filepath.Join(work, "work"), filepath.Join(work, "out"), filepath.Join(work, "probe")
	for _, d := range []string{out, cfg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			res.Error = err.Error()
			return res
		}
	}
	if err := copyTree(repo, src); err != nil {
		res.Error = "copying the repository: " + err.Error()
		return res
	}
	rc := "[run]\nomit =\n"
	for _, o := range spec.Omit {
		rc += "    " + o + "\n"
	}
	if err := os.WriteFile(filepath.Join(cfg, "coveragerc"), []byte(rc), 0o644); err != nil {
		res.Error = err.Error()
		return res
	}
	timeout := spec.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cname := "atriumctl-probe-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	args := []string{"run", "--rm", "--name", cname, "--network", "none", "--cap-drop", "all",
		"--security-opt", "no-new-privileges", "--userns", "keep-id", "--pids-limit", "512",
		"--read-only", "--tmpfs", "/tmp:rw,mode=1777",
		"-v", src + ":/work", "-v", out + ":/out", "-v", cfg + ":/probe:ro", "-w", "/work",
		"-e", "HOME=/tmp", "-e", "PYTHONDONTWRITEBYTECODE=1", "-e", "COVERAGE_FILE=/tmp/.coverage",
		image}
	args = append(args, spec.Command...)
	cmd := exec.Command(r.podman(), args...)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	start := time.Now()
	if err := cmd.Start(); err != nil {
		res.Error = err.Error()
		return res
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-cctx.Done():
		_ = exec.Command(r.podman(), "rm", "-f", "-t", "2", cname).Run()
		err = <-done
		res.Error = "probe timed out or was stopped: " + cctx.Err().Error()
	}
	res.DurationMS = time.Since(start).Milliseconds()
	res.Output = tail(output.String(), 4000)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
	} else if err != nil && res.Error == "" {
		res.Error = err.Error()
	}
	if b, err := os.ReadFile(filepath.Join(out, "junit.xml")); err == nil {
		if err := parseJUnit(b, res); err != nil && res.Error == "" {
			res.Error = "junit report: " + err.Error()
		}
	} else if res.Error == "" {
		res.Error = fmt.Sprintf("no test report (exit %d)", res.ExitCode)
	}
	if b, err := os.ReadFile(filepath.Join(out, "coverage.json")); err == nil {
		if err := parseCoverage(b, res); err != nil && res.Error == "" {
			res.Error = "coverage report: " + err.Error()
		}
	}
	return res
}

// copyTree copies regular files and directories, writable, without symlinks.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(t, 0o755)
		case d.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(t, b, 0o644)
		}
		return nil
	})
}

type junitCase struct {
	Class   string `xml:"classname,attr"`
	Name    string `xml:"name,attr"`
	Failure *struct {
		Message string `xml:"message,attr"`
	} `xml:"failure"`
	Error *struct {
		Message string `xml:"message,attr"`
	} `xml:"error"`
	Skipped *struct{} `xml:"skipped"`
}

type junitSuite struct {
	Cases  []junitCase  `xml:"testcase"`
	Suites []junitSuite `xml:"testsuite"`
}

func parseJUnit(b []byte, res *Result) error {
	var root junitSuite
	if err := xml.Unmarshal(b, &root); err != nil {
		return err
	}
	var visit func(s junitSuite)
	visit = func(s junitSuite) {
		for _, c := range s.Cases {
			res.Tests++
			name := c.Class + "::" + c.Name
			switch {
			case c.Failure != nil:
				res.Failed++
				res.Failures = append(res.Failures, Failure{Test: name, Kind: "failure", Message: tail(c.Failure.Message, 500)})
			case c.Error != nil:
				res.Errors++
				res.Failures = append(res.Failures, Failure{Test: name, Kind: "error", Message: tail(c.Error.Message, 500)})
			case c.Skipped != nil:
				res.Skipped++
			default:
				res.Passed++
			}
		}
		for _, sub := range s.Suites {
			visit(sub)
		}
	}
	visit(root)
	return nil
}

func pct(c, t int) float64 {
	if t == 0 {
		return 0
	}
	return float64(int(float64(c)/float64(t)*10000+0.5)) / 100
}

func parseCoverage(b []byte, res *Result) error {
	var cov struct {
		Files map[string]struct {
			Summary struct {
				Covered    int `json:"covered_lines"`
				Statements int `json:"num_statements"`
				Branches   int `json:"num_branches"`
				CoveredBr  int `json:"covered_branches"`
			} `json:"summary"`
			Missing []int `json:"missing_lines"`
		} `json:"files"`
		Totals struct {
			Covered    int `json:"covered_lines"`
			Statements int `json:"num_statements"`
			Branches   int `json:"num_branches"`
			CoveredBr  int `json:"covered_branches"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(b, &cov); err != nil {
		return err
	}
	t := cov.Totals
	res.Lines = Coverage{Covered: t.Covered, Total: t.Statements, Percent: pct(t.Covered, t.Statements)}
	res.Branches = Coverage{Covered: t.CoveredBr, Total: t.Branches, Percent: pct(t.CoveredBr, t.Branches)}
	for p, f := range cov.Files {
		s := f.Summary
		res.Files = append(res.Files, FileCoverage{Path: filepath.ToSlash(p),
			Lines:   Coverage{Covered: s.Covered, Total: s.Statements, Percent: pct(s.Covered, s.Statements)},
			Branch:  Coverage{Covered: s.CoveredBr, Total: s.Branches, Percent: pct(s.CoveredBr, s.Branches)},
			Missing: f.Missing})
	}
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Path < res.Files[j].Path })
	return nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
