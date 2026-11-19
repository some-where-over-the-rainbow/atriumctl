// Command mini runs the engine-spike scenario on the own minimal engine.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"atriumctl/spikes/engine/common"
	"atriumctl/spikes/engine/mini/engine"
)

// body adapts an engine-neutral typed body to the engine's JSON boundary.
func body[IN, OUT any](f func(context.Context, IN) (OUT, error)) engine.Fn {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in IN
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return f(ctx, in)
	}
}

func hooks(l *common.Log) engine.Hooks {
	return engine.Hooks{
		Start: func(node, item string) { l.Write(common.Rec{Ev: "start", Node: node, Item: item}) },
		End: func(node, item string, err error) {
			r := common.Rec{Ev: "end", Node: node, Item: item}
			if err != nil {
				r.Err = err.Error()
			}
			l.Write(r)
		},
	}
}

func scenario(l *common.Log, width int, journal string) *engine.Engine {
	return &engine.Engine{
		Path:  journal,
		Hooks: hooks(l),
		Nodes: []engine.Node{
			{Name: "question", Fn: body(func(context.Context, string) ([]string, error) { return common.Questions(width), nil })},
			{Name: "gate", Kind: engine.Gate},
			{Name: "worker", Kind: engine.Map, Cap: 3, Fn: body(common.Worker(l))},
			{Name: "synthesize", Fn: body(func(_ context.Context, facts []string) (string, error) {
				out := common.Synthesize(facts)
				l.Write(common.Rec{Ev: "result", Node: "synthesize", Data: out})
				return out, nil
			})},
		},
	}
}

func drive(l *common.Log, e *engine.Engine, crash bool) error {
	_, err := e.Run(context.Background(), "start")
	switch {
	case errors.Is(err, engine.ErrPaused):
		l.Write(common.Rec{Ev: "paused", Node: "gate"})
		if crash {
			common.Die()
		}
		return nil
	case err != nil:
		l.Write(common.Rec{Ev: "error", Err: err.Error()})
		return err
	}
	l.Write(common.Rec{Ev: "done"})
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mini run|resume|continue|timeout [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	db := fs.String("db", "", "journal file")
	logp := fs.String("log", "", "trace log")
	width := fs.Int("width", 5, "questions")
	crash := fs.Bool("crash", false, "SIGKILL self at the pause")
	approve := fs.String("approve", "", "approved question ids")
	_ = fs.Parse(os.Args[2:])
	l := common.OpenLog(*logp)

	var err error
	switch os.Args[1] {
	case "run":
		err = drive(l, scenario(l, *width, *db), *crash)
	case "resume":
		e := scenario(l, *width, *db)
		if err = e.Answer("gate", strings.Split(*approve, ",")); err == nil {
			err = drive(l, e, false)
		}
	case "continue": // after a crash: just run again; the journal says where to go on
		err = drive(l, scenario(l, *width, *db), false)
	case "timeout":
		journal := *logp + ".journal"
		e := &engine.Engine{Path: journal, Hooks: hooks(l), Nodes: []engine.Node{{
			Name: "slow", Timeout: time.Second,
			Fn: body(func(ctx context.Context, _ string) (string, error) {
				return "", common.RunChild(ctx, l, "slow", "sleep", "30")
			}),
		}}}
		err = drive(l, e, false)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
