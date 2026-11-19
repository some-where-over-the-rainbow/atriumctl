package main

// A minimal tool gateway: an MCP server over streamable HTTP (JSON responses
// only, no SSE). Each agent gets its own URL, /mcp/<agentID>, so the gateway
// knows the caller without trusting anything the model says.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ToolCall struct {
	Agent      string          `json:"agent"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	Status     string          `json:"status"` // ok, denied, error
	DenyReason string          `json:"deny_reason,omitempty"`
	Start      time.Time       `json:"ts_start"`
	End        time.Time       `json:"ts_end"`
}

type SpawnFunc func(parentID, prompt string) (string, error)

type Gateway struct {
	Root  string // the work sandbox root; tools act only below it
	Spawn SpawnFunc

	mu    sync.Mutex
	calls []ToolCall
	rpc   []string // every JSON-RPC method seen, "agent method"
}

func (g *Gateway) Calls(agent string) []ToolCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []ToolCall
	for _, c := range g.calls {
		if agent == "" || c.Agent == agent {
			out = append(out, c)
		}
	}
	return out
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

var toolDefs = []map[string]any{
	{
		"name":        "read",
		"description": "Read a text file from the working tree. Path is relative to the tree root.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
	},
	{
		"name":        "ls",
		"description": "List a directory in the working tree. Path is relative to the tree root.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
	},
	{
		"name":        "spawn",
		"description": "Start a sub-agent with the given prompt and return its final answer.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"prompt": map[string]any{"type": "string"}},
			"required":   []string{"prompt"},
		},
	},
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agent := strings.TrimPrefix(r.URL.Path, "/mcp/")
	if agent == "" || strings.Contains(agent, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	default: // no server-initiated stream
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req rpcReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "batch or invalid JSON-RPC not supported", http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	g.rpc = append(g.rpc, agent+" "+req.Method)
	g.mu.Unlock()

	if len(req.ID) == 0 { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	var rpcErr map[string]any
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		result = map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "atriumctl-tool-gateway", "version": "spike"},
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": toolDefs}
	case "tools/call":
		result = g.call(agent, req.Params)
	default:
		rpcErr = map[string]any{"code": -32601, "message": "method not found: " + req.Method}
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func textResult(s string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": s}},
		"isError": isErr,
	}
}

func (g *Gateway) call(agent string, params json.RawMessage) map[string]any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)
	tc := ToolCall{Agent: agent, Tool: p.Name, Args: p.Arguments, Start: time.Now()}
	var out map[string]any
	defer func() {
		tc.End = time.Now()
		g.mu.Lock()
		g.calls = append(g.calls, tc)
		g.mu.Unlock()
	}()

	var a struct {
		Path   string `json:"path"`
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal(p.Arguments, &a)

	switch p.Name {
	case "read", "ls":
		full, ok := g.resolve(a.Path)
		if !ok {
			tc.Status, tc.DenyReason = "denied", "mount"
			return textResult("denied: path is outside the working tree", true)
		}
		if p.Name == "read" {
			b, err := os.ReadFile(full)
			if err != nil {
				tc.Status = "error"
				return textResult(err.Error(), true)
			}
			tc.Status = "ok"
			return textResult(string(b), false)
		}
		ents, err := os.ReadDir(full)
		if err != nil {
			tc.Status = "error"
			return textResult(err.Error(), true)
		}
		var names []string
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
		tc.Status = "ok"
		return textResult(strings.Join(names, "\n"), false)
	case "spawn":
		if g.Spawn == nil {
			tc.Status, tc.DenyReason = "denied", "grant"
			return textResult("denied: spawn not granted", true)
		}
		ans, err := g.Spawn(agent, a.Prompt)
		if err != nil {
			tc.Status = "error"
			return textResult("sub-agent failed: "+err.Error(), true)
		}
		tc.Status = "ok"
		return textResult(ans, false)
	default:
		tc.Status, tc.DenyReason = "denied", "grant"
		out = textResult(fmt.Sprintf("unknown tool %q", p.Name), true)
		return out
	}
}

// resolve maps a tree-relative path to an absolute one, refusing anything
// that leaves the root (absolute paths, .., symlinks out).
func (g *Gateway) resolve(rel string) (string, bool) {
	if rel == "" {
		rel = "."
	}
	if filepath.IsAbs(rel) {
		return "", false
	}
	full := filepath.Join(g.Root, rel)
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		real = full // let the read report "not found"
	}
	root, _ := filepath.EvalSymlinks(g.Root)
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", false
	}
	return real, true
}
