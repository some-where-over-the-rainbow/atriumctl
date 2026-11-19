package run

import (
	"context"
	"errors"
	"time"

	"atriumctl/kernel/engine"
)

// DriveOptions control Drive.
type DriveOptions struct {
	NoWait   bool                    // return at a usage-window wait instead of sleeping
	OnStatus func(engine.Status)     // after every step
	OnAuto   func(approved []string) // gate answered automatically
	OnWait   func(until time.Time)   // before sleeping for the window
}

// Drive runs the graph until it is done, failed, aborted, or needs the
// architect; it answers gates itself in auto mode and sleeps through usage
// windows unless NoWait. The caller must hold the lock and have Started.
func (r *Run) Drive(ctx context.Context, o DriveOptions) (engine.Status, error) {
	for {
		st, err := r.Step(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			return st, err
		}
		if o.OnStatus != nil {
			o.OnStatus(st)
		}
		switch st.State {
		case engine.Blocked:
			if len(st.Gates) > 0 && len(st.Exhausted) == 0 && r.Cfg.GateMode == "auto" {
				prop, err := r.Proposed()
				if err != nil {
					return st, err
				}
				ids := []string{}
				for _, q := range prop {
					ids = append(ids, q.ID)
				}
				if o.OnAuto != nil {
					o.OnAuto(ids)
				}
				if err := r.Answer(GateAnswer{Approve: ids}, "auto"); err != nil {
					return st, err
				}
				continue
			}
			return st, nil
		case engine.Waiting:
			if o.NoWait {
				return st, nil
			}
			if o.OnWait != nil {
				o.OnWait(st.NextWake)
			}
			select {
			case <-time.After(time.Until(st.NextWake)):
				continue
			case <-ctx.Done():
				st.State = engine.Stopped
				return st, nil
			}
		default:
			return st, nil
		}
	}
}
