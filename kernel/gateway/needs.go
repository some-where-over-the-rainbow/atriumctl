package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Statuses only the architect may set (slice §8.3).
var architectStatuses = map[string]bool{"approved": true, "rejected": true, "superseded": true}

type needsFinding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	Need     string `json:"need"`
	Message  string `json:"message"`
}

type needsResponse struct {
	Error  string `json:"error"`
	Report struct {
		Needs []struct {
			ID     string  `json:"id"`
			Status *string `json:"status"`
		} `json:"needs"`
		Findings []needsFinding `json:"findings"`
	} `json:"report"`
	IDs      []string       `json:"ids"`
	Findings []needsFinding `json:"findings"`
}

// needsAPI runs `needs api` with a JSON request.
func (g *Gateway) needsAPI(req map[string]any) (*needsResponse, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.NeedsBin, "api")
	cmd.Stdin = bytes.NewReader(b)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("needs api: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var r needsResponse
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("needs api: %v", err)
	}
	if r.Error != "" {
		return nil, fmt.Errorf("needs api: %s", r.Error)
	}
	return &r, nil
}

func (g *Gateway) needsConfig(a *agentState) (string, error) {
	r, err := resolve(a.Mounts, a.Needs.Config)
	if err != nil {
		return "", fmt.Errorf("needs.toml is not available: %v", err)
	}
	b, err := os.ReadFile(r.host)
	return string(b), err
}

// statuses parses one file's need blocks: id -> status.
func (g *Gateway) statuses(cfg, vpath, text string) (map[string]string, error) {
	r, err := g.needsAPI(map[string]any{"op": "lint", "config": cfg,
		"sources": []map[string]string{{"path": vpath, "text": text}}})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, n := range r.Report.Needs {
		if _, dup := out[n.ID]; dup {
			continue
		}
		s := ""
		if n.Status != nil {
			s = *n.Status
		}
		out[n.ID] = s
	}
	return out, nil
}

// statusRule: agents may write draft and proposed; a status only the
// architect sets may not appear, change or disappear through an agent.
func (g *Gateway) statusRule(a *agentState, vpath, old, new string) error {
	cfg, err := g.needsConfig(a)
	if err != nil {
		return deny("status", "cannot check need statuses: %v", err)
	}
	before, err := g.statuses(cfg, vpath, old)
	if err != nil {
		return deny("status", "cannot check need statuses: %v", err)
	}
	after, err := g.statuses(cfg, vpath, new)
	if err != nil {
		return deny("status", "cannot check need statuses: %v", err)
	}
	var bad []string
	for id, s := range after {
		if architectStatuses[s] && before[id] != s {
			bad = append(bad, fmt.Sprintf("%s set to %s", id, s))
		}
	}
	for id, s := range before {
		if architectStatuses[s] && after[id] != s {
			if _, still := after[id]; still {
				bad = append(bad, fmt.Sprintf("%s changed from %s", id, s))
			} else {
				bad = append(bad, fmt.Sprintf("%s (%s) removed", id, s))
			}
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return deny("status", "only the architect sets approved, rejected or superseded: %s", strings.Join(bad, "; "))
	}
	return nil
}

func (g *Gateway) needsRequest(a *agentState, op string) (map[string]any, error) {
	cfg, err := g.needsConfig(a)
	if err != nil {
		return nil, err
	}
	var sources []map[string]string
	for _, s := range a.Needs.Sources {
		r, err := resolve(a.Mounts, s)
		if err != nil || !r.exist {
			continue
		}
		b, err := readFile(r.host)
		if err != nil {
			continue
		}
		sources = append(sources, map[string]string{"path": r.vpath, "text": string(b)})
	}
	req := map[string]any{"op": op, "config": cfg, "sources": sources}
	if g.RepoFiles != nil {
		req["context"] = map[string]any{"files": g.RepoFiles()}
	}
	return req, nil
}

func (g *Gateway) needsLint(a *agentState, ci *callInfo) (string, error) {
	req, err := g.needsRequest(a, "lint")
	if err != nil {
		return "", err
	}
	r, err := g.needsAPI(req)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, f := range r.Report.Findings {
		switch f.Severity {
		case "error":
			ci.lintErrors++
		case "warning":
			ci.lintWarnings++
		}
		need := ""
		if f.Need != "" {
			need = " " + f.Need
		}
		fmt.Fprintf(&out, "%s:%d: %s[%s]%s: %s\n", f.Path, f.Line, f.Severity, f.Rule, need, f.Message)
	}
	fmt.Fprintf(&out, "%d needs, %d errors, %d warnings\n", len(r.Report.Needs), ci.lintErrors, ci.lintWarnings)
	return out.String(), nil
}

func (g *Gateway) needsQuery(a *agentState, x args) (string, error) {
	req, err := g.needsRequest(a, "query")
	if err != nil {
		return "", err
	}
	q := map[string]any{}
	for k, v := range map[string]string{"type": x.Type, "status": x.Status, "has_incoming": x.HasInc, "without_incoming": x.WithoutInc} {
		if v != "" {
			q[k] = v
		}
	}
	req["query"] = q
	r, err := g.needsAPI(req)
	if err != nil {
		return "", err
	}
	if len(r.IDs) == 0 {
		return "no needs match\n", nil
	}
	return strings.Join(r.IDs, "\n") + "\n", nil
}
