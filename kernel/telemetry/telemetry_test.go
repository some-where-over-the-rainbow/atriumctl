package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atriumctl/kernel/trace"
)

const payload = `{"resourceLogs":[{"resource":{"attributes":[
 {"key":"atriumctl.agent_id","value":{"stringValue":"q-1"}},{"key":"atriumctl.run_id","value":{"stringValue":"r1"}},
 {"key":"atriumctl.stage_attempt","value":{"stringValue":"2"}},{"key":"user.email","value":{"stringValue":"someone@example.com"}}]},
 "scopeLogs":[{"logRecords":[
  {"attributes":[{"key":"event.name","value":{"stringValue":"api_request"}},{"key":"model","value":{"stringValue":"claude-haiku-5-5"}},
   {"key":"input_tokens","value":{"stringValue":"4"}},{"key":"cache_read_tokens","value":{"stringValue":"7163"}},
   {"key":"cost_usd","value":{"stringValue":"0.00019"}},{"key":"user.email","value":{"stringValue":"someone@example.com"}},
   {"key":"organization.id","value":{"stringValue":"org-1"}},{"key":"session.id","value":{"stringValue":"s-1"}}]},
  {"attributes":[{"key":"event.name","value":{"stringValue":"tool_result"}}]}]}]}]}`

func TestModelCallWithoutIdentity(t *testing.T) {
	sink := &trace.Memory{}
	srv := httptest.NewServer(&Receiver{Trace: sink})
	defer srv.Close()
	if _, err := http.Post(srv.URL+"/v1/logs", "application/json", strings.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	ev := sink.Events("model_call")
	if len(ev) != 1 {
		t.Fatalf("events: %+v", sink.Events(""))
	}
	e := ev[0]
	if e.AgentID != "q-1" || e.RunID != "r1" || e.StageAttempt != 2 || e.Source != trace.SourceHarness {
		t.Fatalf("%+v", e)
	}
	if e.Fields["gen_ai.request.model"] != "claude-haiku-5-5" || e.Fields["cache_read_tokens"] != int64(7163) || e.Fields["cost_estimate"] != 0.00019 {
		t.Fatalf("%+v", e.Fields)
	}
	for k, v := range e.Fields {
		if strings.Contains(k, "email") || strings.Contains(k, "organization") || v == "someone@example.com" {
			t.Fatalf("identity kept: %s=%v", k, v)
		}
	}
}
