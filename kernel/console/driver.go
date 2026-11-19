package console

import (
	"context"
	"sync"

	"atriumctl/kernel/run"
)

// driving is a run the console drives in the background.
type driving struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	done   bool
	err    string
}

func (d *driving) active() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.done
}

func (d *driving) lastErr() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// drive runs rn (whose lock the caller holds) until it needs the
// architect, then releases it.
func (s *Server) drive(rn *run.Run) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &driving{cancel: cancel}
	s.mu.Lock()
	s.drivers[rn.Cfg.ID] = d
	s.mu.Unlock()
	go func() {
		defer rn.Close()
		defer cancel()
		var errText string
		if err := rn.Start(); err != nil {
			errText = err.Error()
		} else if st, err := rn.Drive(ctx, run.DriveOptions{}); err != nil {
			errText = err.Error()
		} else {
			s.logf("console: run %s %s", rn.Cfg.ID, st.State)
		}
		if errText != "" {
			s.logf("console: run %s: %s", rn.Cfg.ID, errText)
		}
		d.mu.Lock()
		d.done, d.err = true, errText
		d.mu.Unlock()
	}()
}

func (s *Server) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.drivers {
		d.cancel()
	}
}
