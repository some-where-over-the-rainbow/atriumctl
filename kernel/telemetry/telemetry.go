// Package telemetry receives Claude Code's OpenTelemetry export (OTLP over
// HTTP with JSON) and turns each api_request log event into a model_call
// trace event. The data is self-reported by the harness.
//
// Account identity (email, account and organisation ids) arrives on every
// event; it is dropped on receipt (docs/spike-claude-code.md §4.7). Agents
// are identified by the resource attributes the executor sets through
// OTEL_RESOURCE_ATTRIBUTES (atriumctl.agent_id, atriumctl.run_id, ...).
package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"atriumctl/kernel/trace"
)

// Dropped are attributes never stored.
var Dropped = map[string]bool{
	"user.email": true, "user.account_uuid": true, "user.account_id": true, "user.id": true,
	"organization.id": true, "anthropic.custom_base_url": true,
}

type Receiver struct {
	Trace trace.Sink
}

type kv struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

func flat(attrs []kv) map[string]string {
	m := map[string]string{}
	for _, a := range attrs {
		if Dropped[a.Key] {
			continue
		}
		for _, v := range a.Value {
			switch x := v.(type) {
			case string:
				m[a.Key] = x
			case float64:
				m[a.Key] = strconv.FormatFloat(x, 'f', -1, 64)
			case bool:
				m[a.Key] = strconv.FormatBool(x)
			}
		}
	}
	return m
}

func (rc *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	w.Header().Set("Content-Type", "application/json")
	defer func() { _, _ = w.Write([]byte("{}")) }()
	if r.URL.Path != "/v1/logs" {
		return // metrics and traces are not used
	}
	var p struct {
		ResourceLogs []struct {
			Resource  struct{ Attributes []kv } `json:"resource"`
			ScopeLogs []struct {
				LogRecords []struct {
					Attributes []kv `json:"attributes"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if json.Unmarshal(body, &p) != nil {
		return
	}
	for _, rl := range p.ResourceLogs {
		res := flat(rl.Resource.Attributes)
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				a := flat(lr.Attributes)
				if !strings.HasSuffix(a["event.name"], "api_request") {
					continue
				}
				rc.Trace.Emit(modelCall(res, a))
			}
		}
	}
}

func num(s string) any {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return nil
}

func modelCall(res, a map[string]string) trace.Event {
	f := map[string]any{"gen_ai.request.model": a["model"], "session_id": a["session.id"], "request_id": a["request_id"]}
	for k, name := range map[string]string{
		"input_tokens": "input_tokens", "output_tokens": "output_tokens",
		"cache_read_tokens": "cache_read_tokens", "cache_creation_tokens": "cache_write_tokens",
		"cost_usd": "cost_estimate", "duration_ms": "duration_ms", "ttft_ms": "ttft_ms",
	} {
		if v := num(a[k]); v != nil {
			f[name] = v
		}
	}
	ts := time.Now()
	if t, err := time.Parse(time.RFC3339Nano, a["event.timestamp"]); err == nil {
		ts = t
	}
	attempt, _ := strconv.Atoi(res["atriumctl.stage_attempt"])
	return trace.Event{
		Type: "model_call", SchemaVersion: trace.SchemaVersion, Source: trace.SourceHarness, TsStart: ts,
		RunID: res["atriumctl.run_id"], Stage: res["atriumctl.stage"], StageAttempt: attempt, Role: res["atriumctl.role"],
		AgentID: res["atriumctl.agent_id"], ParentAgentID: res["atriumctl.parent_agent_id"], Fields: f,
	}
}
