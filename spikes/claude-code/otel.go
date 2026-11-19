package main

// A minimal OTLP/HTTP-JSON receiver for Claude Code's telemetry.

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
)

type OTelRecord struct {
	Signal string            `json:"signal"` // logs, metrics
	Name   string            `json:"name"`   // event or metric name
	Attrs  map[string]string `json:"attrs"`
}

type OTelSink struct {
	mu      sync.Mutex
	records []OTelRecord
	raw     map[string]int // request count per path
}

func (o *OTelSink) Records() []OTelRecord {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]OTelRecord(nil), o.records...)
}

type kv struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

func flat(attrs []kv) map[string]string {
	m := map[string]string{}
	for _, a := range attrs {
		for _, v := range a.Value {
			switch x := v.(type) {
			case string:
				m[a.Key] = x
			case float64:
				m[a.Key] = strconv.FormatFloat(x, 'f', -1, 64)
			case bool:
				m[a.Key] = strconv.FormatBool(x)
			default:
				b, _ := json.Marshal(x)
				m[a.Key] = string(b)
			}
		}
	}
	return m
}

func (o *OTelSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	o.mu.Lock()
	if o.raw == nil {
		o.raw = map[string]int{}
	}
	o.raw[r.URL.Path]++
	o.mu.Unlock()

	switch r.URL.Path {
	case "/v1/logs":
		var p struct {
			ResourceLogs []struct {
				Resource  struct{ Attributes []kv } `json:"resource"`
				ScopeLogs []struct {
					LogRecords []struct {
						Body       map[string]any `json:"body"`
						Attributes []kv           `json:"attributes"`
					} `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		_ = json.Unmarshal(body, &p)
		o.mu.Lock()
		for _, rl := range p.ResourceLogs {
			res := flat(rl.Resource.Attributes)
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					a := flat(lr.Attributes)
					for k, v := range res {
						if _, ok := a[k]; !ok {
							a["resource."+k] = v
						}
					}
					name := a["event.name"]
					if name == "" {
						if s, ok := lr.Body["stringValue"].(string); ok {
							name = s
						}
					}
					o.records = append(o.records, OTelRecord{Signal: "logs", Name: name, Attrs: a})
				}
			}
		}
		o.mu.Unlock()
	case "/v1/metrics":
		var p struct {
			ResourceMetrics []struct {
				ScopeMetrics []struct {
					Metrics []struct {
						Name string `json:"name"`
					} `json:"metrics"`
				} `json:"scopeMetrics"`
			} `json:"resourceMetrics"`
		}
		_ = json.Unmarshal(body, &p)
		o.mu.Lock()
		for _, rm := range p.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					o.records = append(o.records, OTelRecord{Signal: "metrics", Name: m.Name})
				}
			}
		}
		o.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

func telemetryEnv(endpoint string) []string {
	return []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_LOGS_EXPORTER=otlp",
		"OTEL_METRICS_EXPORTER=otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/json",
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + endpoint,
		"OTEL_LOGS_EXPORT_INTERVAL=1000",
		"OTEL_METRIC_EXPORT_INTERVAL=1000",
	}
}
