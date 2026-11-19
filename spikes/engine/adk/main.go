// Command adk runs the engine-spike scenario on Google ADK for Go v2
// (workflow package, function nodes only; no model agents).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"google.golang.org/genai"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"
	"google.golang.org/adk/v2/workflow"

	"atriumctl/spikes/engine/common"
)

const (
	appName   = "atriumctl-spike"
	userID    = "architect"
	sessionID = "run-1"
	gateID    = "gate"
)

// fn adapts an engine-neutral body to an ADK function-node body.
func fn[IN, OUT any](f func(context.Context, IN) (OUT, error)) func(agent.Context, IN) (OUT, error) {
	return func(ctx agent.Context, in IN) (OUT, error) { return f(ctx, in) }
}

func build(l *common.Log, width int) (agent.Agent, error) {
	question := workflow.NewFunctionNode("question", fn(common.Traced(l, "question",
		func(_ context.Context, _ any) ([]string, error) { return common.Questions(width), nil })), workflow.NodeConfig{})

	gate := workflow.NewEmittingFunctionNode[any, []string]("gate",
		func(ctx agent.Context, in any, emit func(*session.Event) error) ([]string, error) {
			l.Write(common.Rec{Ev: "start", Node: "gate"})
			reply, err := workflow.ResumeOrRequestInput(ctx, emit, session.RequestInput{
				InterruptID: gateID, Message: "approve questions", Payload: in,
			})
			if err != nil {
				l.Write(common.Rec{Ev: "end", Node: "gate", Err: err.Error()})
				return nil, err
			}
			ok := common.Approved(reply)
			l.Write(common.Rec{Ev: "end", Node: "gate", Data: ok})
			return ok, nil
		}, workflow.NodeConfig{RerunOnResume: new(true)})

	worker := workflow.NewFunctionNode("worker", fn(common.Traced(l, "worker", common.Worker(l))), workflow.NodeConfig{})
	workers, err := workflow.NewParallelWorker("workers", worker, 3, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}

	synth := workflow.NewFunctionNode("synthesize", fn(common.Traced(l, "synthesize",
		func(_ context.Context, in any) (string, error) {
			var facts []string
			for _, x := range common.Approved(in) {
				facts = append(facts, x)
			}
			out := common.Synthesize(facts)
			l.Write(common.Rec{Ev: "result", Node: "synthesize", Data: out})
			return out, nil
		})), workflow.NodeConfig{})

	return workflowagent.New(workflowagent.Config{
		Name:  "slice1",
		Edges: workflow.Chain(workflow.Start, question, gate, workers, synth),
	})
}

func buildTimeout(l *common.Log) (agent.Agent, error) {
	slow := workflow.NewFunctionNode("slow", fn(common.Traced(l, "slow",
		func(ctx context.Context, _ any) (string, error) {
			return "", common.RunChild(ctx, l, "slow", "sleep", "30")
		})), workflow.NodeConfig{Timeout: time.Second})
	return workflowagent.New(workflowagent.Config{Name: "timeout", Edges: workflow.Chain(workflow.Start, slow)})
}

func newRunner(root agent.Agent, db string) (*runner.Runner, error) {
	var svc session.Service = session.InMemoryService()
	if db != "" {
		s, err := database.NewSessionService(sqlite.Open(db), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			return nil, err
		}
		if err := database.AutoMigrate(s); err != nil {
			return nil, err
		}
		svc = s
	}
	return runner.New(runner.Config{AppName: appName, Agent: root, SessionService: svc, AutoCreateSession: true})
}

func drive(l *common.Log, r *runner.Runner, msg *genai.Content, crash bool) error {
	paused := false
	var runErr error
	for ev, err := range r.Run(context.Background(), userID, sessionID, msg, agent.RunConfig{}) {
		if err != nil {
			runErr = errors.Join(runErr, err)
			continue
		}
		if ev.RequestedInput != nil && !paused {
			paused = true
			l.Write(common.Rec{Ev: "paused", Node: "gate"})
			if crash {
				common.Die()
			}
		}
	}
	if runErr != nil {
		l.Write(common.Rec{Ev: "error", Err: runErr.Error()})
		return runErr
	}
	if !paused {
		l.Write(common.Rec{Ev: "done"})
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: adk run|resume|timeout [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	db := fs.String("db", "", "sqlite file")
	logp := fs.String("log", "", "trace log")
	width := fs.Int("width", 5, "questions")
	crash := fs.Bool("crash", false, "SIGKILL self at the pause")
	approve := fs.String("approve", "", "approved question ids")
	_ = fs.Parse(os.Args[2:])
	l := common.OpenLog(*logp)

	var err error
	switch os.Args[1] {
	case "run", "resume", "continue":
		var root agent.Agent
		if root, err = build(l, *width); err == nil {
			var r *runner.Runner
			if r, err = newRunner(root, *db); err == nil {
				if os.Args[1] == "run" {
					err = drive(l, r, genai.NewContentFromText("start", genai.RoleUser), *crash)
				} else { // resume, or continue after a crash: re-send the stored answer
					ids := []any{}
					for _, s := range strings.Split(*approve, ",") {
						ids = append(ids, s)
					}
					err = drive(l, r, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{
						FunctionResponse: &genai.FunctionResponse{
							ID: gateID, Name: workflow.WorkflowInputFunctionCallName,
							Response: map[string]any{"payload": ids},
						},
					}}}, false)
				}
			}
		}
	case "timeout":
		var root agent.Agent
		if root, err = buildTimeout(l); err == nil {
			var r *runner.Runner
			if r, err = newRunner(root, ""); err == nil {
				err = drive(l, r, genai.NewContentFromText("start", genai.RoleUser), false)
			}
		}
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
