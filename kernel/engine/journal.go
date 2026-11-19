package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// Record is one journal entry. Kinds:
//
//	header    graph hash (first record)
//	input     the run's input
//	done      node finished; Out is its output
//	item      map item finished
//	itemx     map item exhausted; Out is its partial output
//	failed    node failed; Err
//	gate      gate opened; Out is its input
//	answer    gate answered; Out is the answer
//	wait      node must wait until Until (e.g. subscription window)
//	exhausted node hit a cap; Out is its partial output
//	decide    architect's decision on an exhausted node: accept, retry, abort
//	back      back-edge taken; Node=From, To, Count
//	abort     run aborted
type Record struct {
	Kind    string          `json:"k"`
	Node    string          `json:"n,omitempty"`
	Attempt int             `json:"a,omitempty"`
	Item    int             `json:"i,omitempty"`
	Out     json.RawMessage `json:"o,omitempty"`
	Err     string          `json:"err,omitempty"`
	Until   time.Time       `json:"until,omitzero"`
	Reason  string          `json:"why,omitempty"`
	To      string          `json:"to,omitempty"`
	Count   int             `json:"c,omitempty"`
	Hash    string          `json:"h,omitempty"`
	At      time.Time       `json:"t"`
}

// Journal is the engine's durable state. Append must be durable on return.
type Journal interface {
	Load() ([]Record, error)
	Append(Record) error
}

// FileJournal is an append-only JSON-lines file, synced after every record.
type FileJournal struct {
	Path string
	mu   sync.Mutex
	f    *os.File
}

func (j *FileJournal) Load() ([]Record, error) {
	f, err := os.Open(j.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue // a torn last line after a crash
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func (j *FileJournal) Append(r Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		if err := j.open(); err != nil {
			return err
		}
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

// open opens the file for appending, first cutting off a torn last line
// (a write interrupted by a crash), so the next record starts on its own line.
func (j *FileJournal) open() error {
	f, err := os.OpenFile(j.Path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if size := st.Size(); size > 0 {
		const chunk = 64 << 10
		end := size
		for end > 0 {
			start := max(end-chunk, 0)
			buf := make([]byte, end-start)
			if _, err := f.ReadAt(buf, start); err != nil {
				f.Close()
				return err
			}
			if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
				end = start + int64(i) + 1
				break
			}
			end = start
		}
		if end != size {
			if err := f.Truncate(end); err != nil {
				f.Close()
				return err
			}
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return err
	}
	j.f = f
	return nil
}

func (j *FileJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

// MemJournal is for tests.
type MemJournal struct {
	mu   sync.Mutex
	Recs []Record
}

func (m *MemJournal) Load() ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Record(nil), m.Recs...), nil
}

func (m *MemJournal) Append(r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Recs = append(m.Recs, r)
	return nil
}
