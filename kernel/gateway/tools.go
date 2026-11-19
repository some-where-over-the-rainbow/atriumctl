package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxReadLines   = 2000
	maxFileBytes   = 4 << 20
	maxGrepMatches = 500
	maxGlobResults = 1000
	maxWalkFiles   = 50000
)

func schema(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{} // "required": null is not valid JSON schema; Claude Code then drops every tool
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

type toolDef struct {
	desc   string
	schema map[string]any
}

var tools = map[string]toolDef{
	"read": {"Read a text file. Lines are numbered. Paths are relative to the working tree root.",
		schema(map[string]any{"path": str("file path"), "offset": num("first line, 1-based"), "limit": num("number of lines")}, "path")},
	"ls": {"List a directory. Directories end with /.",
		schema(map[string]any{"path": str("directory path; empty for the root")})},
	"glob": {"Find files whose path matches a pattern; ** matches any number of directories.",
		schema(map[string]any{"pattern": str("e.g. src/**/*.py"), "path": str("directory to search; empty for the root")}, "pattern")},
	"grep": {"Search file contents with a regular expression (RE2 syntax). Returns path:line:text.",
		schema(map[string]any{"pattern": str("regular expression"), "path": str("directory or file; empty for the root"),
			"glob": str("only files matching this pattern"), "ignore_case": flag("case-insensitive")}, "pattern")},
	"write": {"Write a whole file. An existing file must have been read first and be unchanged since.",
		schema(map[string]any{"path": str("file path"), "content": str("full new content")}, "path", "content")},
	"edit": {"Replace one exact occurrence of old_string with new_string in a file you have read.",
		schema(map[string]any{"path": str("file path"), "old_string": str("exact text to replace"),
			"new_string": str("replacement"), "replace_all": flag("replace every occurrence")}, "path", "old_string", "new_string")},
	"needs_lint": {"Lint the need blocks in your artifacts. Returns findings with file and line.",
		schema(map[string]any{})},
	"needs_query": {"List need ids matching all given conditions.",
		schema(map[string]any{"type": str("need type, e.g. q or fact"), "status": str("status"),
			"has_incoming": str("link name, e.g. answers"), "without_incoming": str("link name, e.g. answers")})},
	"spawn": {"Start a sub-agent with a prompt and return its final answer.",
		schema(map[string]any{"prompt": str("the sub-agent's task")}, "prompt")},
}

// denied is a refusal by one of the layers.
type denied struct {
	layer string // grant, lease, mount, status, cas
	msg   string
}

func (d *denied) Error() string { return fmt.Sprintf("denied (%s): %s", d.layer, d.msg) }

func deny(layer, f string, a ...any) error { return &denied{layer, fmt.Sprintf(f, a...)} }

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// callInfo collects what the trace records about one call.
type callInfo struct {
	path         string
	pathClass    string
	bytesWritten int
	lines        int
	hashBefore   string
	hashAfter    string
	lintErrors   int
	lintWarnings int
	child        string
}

type args struct {
	Path       string `json:"path"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
	Pattern    string `json:"pattern"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignore_case"`
	Content    string `json:"content"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
	Prompt     string `json:"prompt"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	HasInc     string `json:"has_incoming"`
	WithoutInc string `json:"without_incoming"`
}

func (g *Gateway) run(ctx context.Context, a *agentState, tool string, raw json.RawMessage, ci *callInfo) (string, error) {
	if _, known := tools[tool]; !known || !a.granted(tool) {
		return "", deny("grant", "tool %q is not granted to role %s", tool, a.Role)
	}
	var x args
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &x); err != nil {
			return "", fmt.Errorf("bad arguments: %w", err)
		}
	}
	switch tool {
	case "read":
		return g.read(a, x, ci)
	case "ls":
		return g.ls(a, x, ci)
	case "glob":
		return g.glob(a, x, ci)
	case "grep":
		return g.grep(a, x, ci)
	case "write", "edit":
		return g.write(a, tool, x, ci)
	case "needs_lint":
		return g.needsLint(a, ci)
	case "needs_query":
		return g.needsQuery(a, x)
	case "spawn":
		if a.Spawn == nil {
			return "", deny("grant", "spawn is not available")
		}
		ci.child = "spawned"
		return a.Spawn(ctx, a.Agent, x.Prompt)
	}
	return "", fmt.Errorf("unhandled tool %q", tool)
}

func (g *Gateway) open(a *agentState, p string, ci *callInfo) (resolved, error) {
	r, err := resolve(a.Mounts, p)
	if err != nil {
		return r, deny("mount", "%s: %v", clean(p), err)
	}
	ci.path, ci.pathClass = r.vpath, r.mount.At
	if ci.pathClass == "" {
		ci.pathClass = "/"
	}
	return r, nil
}

func readFile(host string) ([]byte, error) {
	st, err := os.Stat(host)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("is a directory")
	}
	if st.Size() > maxFileBytes {
		return nil, fmt.Errorf("file is larger than %d bytes", maxFileBytes)
	}
	return os.ReadFile(host)
}

func (g *Gateway) read(a *agentState, x args, ci *callInfo) (string, error) {
	r, err := g.open(a, x.Path, ci)
	if err != nil {
		return "", err
	}
	b, err := readFile(r.host)
	if err != nil {
		return "", fmt.Errorf("%s: %v", r.vpath, err)
	}
	a.saw(r.vpath, digest(b))
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := max(x.Offset, 1)
	limit := x.Limit
	if limit <= 0 || limit > maxReadLines {
		limit = maxReadLines
	}
	var out strings.Builder
	end := min(start-1+limit, len(lines))
	for i := start - 1; i < end; i++ {
		fmt.Fprintf(&out, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&out, "… %d more lines; use offset %d\n", len(lines)-end, end+1)
	}
	if len(lines) == 0 {
		out.WriteString("(empty file)\n")
	}
	return out.String(), nil
}

func (g *Gateway) ls(a *agentState, x args, ci *callInfo) (string, error) {
	if v := clean(x.Path); isVirtualDir(a.Mounts, v) {
		ci.path, ci.pathClass = v, v
		return strings.Join(sortedNames(nil, children(a.Mounts, v)), "\n") + "\n", nil
	}
	r, err := g.open(a, x.Path, ci)
	if err != nil {
		return "", err
	}
	ents, err := os.ReadDir(r.host)
	if err != nil {
		return "", fmt.Errorf("%s: %v", r.vpath, err)
	}
	var keep []os.DirEntry
	for _, e := range ents {
		if e.Name() != ".git" {
			keep = append(keep, e)
		}
	}
	return strings.Join(sortedNames(keep, children(a.Mounts, r.vpath)), "\n") + "\n", nil
}

func (g *Gateway) glob(a *agentState, x args, ci *callInfo) (string, error) {
	r, err := g.open(a, x.Path, ci)
	if err != nil {
		return "", err
	}
	var out []string
	walk(r, maxWalkFiles, func(v, _ string) bool {
		rel := strings.TrimPrefix(strings.TrimPrefix(v, r.vpath), "/")
		if globMatch(x.Pattern, rel) || globMatch(x.Pattern, v) {
			out = append(out, v)
		}
		return len(out) < maxGlobResults
	})
	if len(out) == 0 {
		return "no files match\n", nil
	}
	return strings.Join(out, "\n") + "\n", nil
}

func (g *Gateway) grep(a *agentState, x args, ci *callInfo) (string, error) {
	pat := x.Pattern
	if x.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return "", fmt.Errorf("pattern: %v", err)
	}
	r, err := g.open(a, x.Path, ci)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	n := 0
	visit := func(v, host string) bool {
		rel := strings.TrimPrefix(strings.TrimPrefix(v, r.vpath), "/")
		if x.Glob != "" && !globMatch(x.Glob, rel) && !globMatch(x.Glob, v) && !globMatch(x.Glob, filepath.Base(v)) {
			return true
		}
		b, err := readFile(host)
		if err != nil || bytes.IndexByte(b, 0) >= 0 {
			return true // unreadable or binary
		}
		for i, line := range strings.Split(string(b), "\n") {
			if re.MatchString(line) {
				fmt.Fprintf(&out, "%s:%d:%s\n", v, i+1, line)
				if n++; n >= maxGrepMatches {
					fmt.Fprintf(&out, "… stopped after %d matches\n", n)
					return false
				}
			}
		}
		return true
	}
	if st, err := os.Stat(r.host); err == nil && !st.IsDir() {
		visit(r.vpath, r.host)
	} else {
		walk(r, maxWalkFiles, visit)
	}
	if n == 0 {
		return "no matches\n", nil
	}
	return out.String(), nil
}

// write handles write and edit: grant (checked by run), lease, mount,
// compare-and-swap against what the agent last saw, then the status rule.
func (g *Gateway) write(a *agentState, tool string, x args, ci *callInfo) (string, error) {
	v := clean(x.Path)
	if !g.lease(a.Agent, v, "rw") {
		return "", deny("lease", "no rw lease on %s", v)
	}
	r, err := g.open(a, x.Path, ci)
	if err != nil {
		return "", err
	}
	if !r.mount.Writable {
		return "", deny("mount", "%s is mounted read-only", r.vpath)
	}
	if st, err := os.Stat(r.host); err == nil && st.IsDir() {
		return "", fmt.Errorf("%s is a directory", r.vpath)
	}
	unlock := g.lock(r.host)
	defer unlock()

	old, err := os.ReadFile(r.host)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if exists {
		ci.hashBefore = digest(old)
		seen, ok := a.lastSeen(r.vpath)
		if !ok {
			return "", deny("cas", "%s exists; read it before changing it", r.vpath)
		}
		if seen != ci.hashBefore {
			return "", deny("cas", "%s changed since you last read it; read it again", r.vpath)
		}
	} else if tool == "edit" {
		return "", fmt.Errorf("%s does not exist", r.vpath)
	}
	content := x.Content
	if tool == "edit" {
		if x.OldString == "" {
			return "", errors.New("old_string is empty")
		}
		n := strings.Count(string(old), x.OldString)
		switch {
		case n == 0:
			return "", fmt.Errorf("old_string not found in %s", r.vpath)
		case n > 1 && !x.ReplaceAll:
			return "", fmt.Errorf("old_string occurs %d times in %s; make it unique or set replace_all", n, r.vpath)
		}
		content = strings.Replace(string(old), x.OldString, x.NewString, map[bool]int{true: -1, false: 1}[x.ReplaceAll])
	}
	if a.Needs != nil && strings.HasSuffix(r.vpath, ".md") {
		if err := g.statusRule(a, r.vpath, string(old), content); err != nil {
			return "", err
		}
	}
	if err := atomicWrite(r.host, []byte(content)); err != nil {
		return "", err
	}
	ci.bytesWritten = len(content)
	ci.lines = strings.Count(content, "\n")
	ci.hashAfter = digest([]byte(content))
	a.saw(r.vpath, ci.hashAfter)
	verb := map[string]string{"write": "wrote", "edit": "edited"}[tool]
	return fmt.Sprintf("%s %s (%d bytes)\n", verb, r.vpath, len(content)), nil
}

func atomicWrite(host string, b []byte) error {
	dir := filepath.Dir(host)
	tmp, err := os.CreateTemp(dir, ".atriumctl-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if st, err := os.Stat(host); err == nil {
		_ = os.Chmod(tmp.Name(), st.Mode().Perm())
	}
	return os.Rename(tmp.Name(), host)
}
