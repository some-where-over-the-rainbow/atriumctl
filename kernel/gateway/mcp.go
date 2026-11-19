package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"atriumctl/kernel/trace"
)

// MCP over streamable HTTP with plain JSON responses: initialize,
// notifications, tools/list, tools/call, ping. That is all Claude Code
// needs (docs/spike-claude-code.md §4.10).

type rpcReq struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a := g.agent(strings.TrimPrefix(r.URL.Path, "/mcp/"))
	if a == nil {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req rpcReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON-RPC", http.StatusBadRequest)
		return
	}
	if len(req.ID) == 0 {
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
			"serverInfo":      map[string]any{"name": "atriumctl-tool-gateway", "version": "1"},
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": g.list(a)}
	case "tools/call":
		result = g.call(r, a, req.Params)
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

func (g *Gateway) list(a *agentState) []map[string]any {
	names := append([]string(nil), a.Tools...)
	sort.Strings(names)
	var out []map[string]any
	for _, n := range names {
		d := tools[n]
		out = append(out, map[string]any{"name": n, "description": d.desc, "inputSchema": d.schema})
	}
	return out
}

func (g *Gateway) call(r *http.Request, a *agentState, params json.RawMessage) map[string]any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)
	start := g.Now()
	var ci callInfo
	text, err := g.run(r.Context(), a, p.Name, p.Arguments, &ci)
	end := g.Now()

	status, reason := "ok", ""
	var d *denied
	switch {
	case errors.As(err, &d):
		status, reason, text = "denied", d.layer, d.Error()
	case err != nil:
		status, text = "error", err.Error()
	}
	f := map[string]any{
		"tool": p.Name, "status": status, "args_bytes": len(p.Arguments), "args_digest": digest(p.Arguments)[:16],
		"result_bytes": len(text), "exec_ms": end.Sub(start).Milliseconds(),
	}
	set := func(k string, v any, ok bool) {
		if ok {
			f[k] = v
		}
	}
	set("deny_reason", reason, reason != "")
	set("path", ci.path, ci.path != "")
	set("path_class", ci.pathClass, ci.pathClass != "")
	set("bytes_written", ci.bytesWritten, ci.bytesWritten > 0)
	set("hash_before", ci.hashBefore, ci.hashBefore != "")
	set("hash_after", ci.hashAfter, ci.hashAfter != "")
	set("lint_errors", ci.lintErrors, p.Name == "needs_lint")
	set("lint_warnings", ci.lintWarnings, p.Name == "needs_lint")
	g.Trace.Emit(trace.Event{
		Type: "tool_call", SchemaVersion: trace.SchemaVersion, Source: trace.SourceGateway,
		TsStart: start, TsEnd: end, RunID: a.RunID, Stage: a.Stage, StageAttempt: a.StageAttempt,
		Role: a.Role, AgentID: a.ID, ParentAgentID: a.ParentID, Fields: f,
	})
	if ci.bytesWritten > 0 {
		g.Trace.Emit(trace.Event{
			Type: "artifact", SchemaVersion: trace.SchemaVersion, Source: trace.SourceGateway,
			TsStart: end, RunID: a.RunID, Stage: a.Stage, StageAttempt: a.StageAttempt, Role: a.Role, AgentID: a.ID,
			Fields: map[string]any{"path": ci.path, "size": ci.bytesWritten, "hash": ci.hashAfter,
				"lines": ci.lines, "tool": p.Name},
		})
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": err != nil,
	}
}
