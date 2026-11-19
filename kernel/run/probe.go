package run

import (
	"context"
	"encoding/json"
	"os"

	"atriumctl/kernel/def"
	"atriumctl/kernel/engine"
	"atriumctl/kernel/probe"
)

// probeStage runs a declared probe on the repository snapshot. A probe
// that cannot run reports it in its result instead of failing the run: a
// broken test setup must not cancel the agents.
func (r *Run) probeStage(st def.Stage) engine.Fn {
	return func(ctx context.Context, in engine.Input) (any, error) {
		spec := r.Def.Probes[st.Probe]
		dir := r.path("probe")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		log, _ := os.Create(r.path("probe", st.Probe+"-build.log"))
		pr := &probe.Runner{}
		if log != nil {
			pr.Log = log
			defer log.Close()
		}
		var res *probe.Result
		img, err := pr.EnsureImage(ctx, spec, r.path("repo"), dir)
		if err != nil {
			res = &probe.Result{Probe: st.Probe, Command: spec.Command, Error: err.Error()}
		} else {
			res = pr.Run(ctx, st.Probe, spec, img, r.path("repo"), dir)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err() // stopped: run it again on resume
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(r.path("probe", st.Probe+".json"), b, 0o600); err != nil {
			return nil, err
		}
		f := map[string]any{"probe": st.Probe, "image": res.Image, "command": res.Command, "exit_code": res.ExitCode,
			"tests": res.Tests, "passed": res.Passed, "failed": res.Failed, "errors": res.Errors, "skipped": res.Skipped,
			"line_coverage": res.Lines.Percent, "branch_coverage": res.Branches.Percent, "files": len(res.Files),
			"duration_ms": res.DurationMS}
		if res.Error != "" {
			f["error"] = res.Error
		}
		r.emit("probe_result", st.Name, in.Attempt, f)
		return map[string]any{"probe": st.Probe, "tests": res.Tests, "failed": res.Failed + res.Errors,
			"lines": res.Lines.Percent, "branches": res.Branches.Percent, "error": res.Error}, nil
	}
}
