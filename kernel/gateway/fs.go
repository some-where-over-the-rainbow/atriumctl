package gateway

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// clean normalises a path as the agent wrote it: forward slashes, no
// leading slash or "./", "" for the root. ".." that would leave the root
// stays and is refused by resolve.
func clean(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	p = path.Clean("/" + p) // "/.." collapses to "/"; leaving the root is impossible after this
	p = strings.TrimPrefix(p, "/")
	return p
}

var errNotMounted = errors.New("not available in this sandbox")

type resolved struct {
	vpath string
	mount Mount
	host  string
	exist bool
}

// resolve maps a virtual path to a host path through the mount table:
// longest mount prefix wins; the result must stay inside the mount, also
// after following symlinks.
func resolve(mounts []Mount, raw string) (resolved, error) {
	if strings.Contains(raw, "\x00") {
		return resolved{}, errNotMounted
	}
	v := clean(raw)
	best := -1
	for i, m := range mounts {
		if m.At == "" || v == m.At || strings.HasPrefix(v, m.At+"/") {
			if best < 0 || len(m.At) > len(mounts[best].At) {
				best = i
			}
		}
	}
	if best < 0 || reservedBelow(mounts, v, mounts[best].At) {
		return resolved{}, errNotMounted
	}
	m := mounts[best]
	rel := strings.TrimPrefix(strings.TrimPrefix(v, m.At), "/")
	root, err := filepath.EvalSymlinks(m.Host)
	if err != nil {
		return resolved{}, errNotMounted
	}
	st, err := os.Stat(root)
	if err != nil {
		return resolved{}, errNotMounted
	}
	if !st.IsDir() {
		if rel != "" {
			return resolved{}, errNotMounted // a file mount has no children
		}
		return resolved{vpath: v, mount: m, host: root, exist: true}, nil
	}
	host := filepath.Join(root, filepath.FromSlash(rel))
	real, err := filepath.EvalSymlinks(host)
	exist := err == nil
	if errors.Is(err, fs.ErrNotExist) {
		// A new file: its parent must resolve inside the mount.
		parent, perr := filepath.EvalSymlinks(filepath.Dir(host))
		if perr != nil {
			return resolved{vpath: v, mount: m, host: host}, nil
		}
		real = filepath.Join(parent, filepath.Base(host))
	} else if err != nil {
		return resolved{}, errNotMounted
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return resolved{}, errNotMounted
	}
	return resolved{vpath: v, mount: m, host: real, exist: exist}, nil
}

// virtualDirs are the directories that hold mount points (".atriumctl" for a
// mount at ".atriumctl/task.md"). They exist only through their mounts: a path
// inside one never falls back to a shorter mount such as the repository,
// so a repository file cannot appear where an unmounted artifact would be.
func virtualDirs(mounts []Mount) map[string]bool {
	out := map[string]bool{}
	for _, m := range mounts {
		for d := path.Dir(m.At); m.At != "" && d != "." && d != "/"; d = path.Dir(d) {
			out[d] = true
		}
	}
	return out
}

// reservedBelow: v lies in a virtual directory deeper than the mount at.
func reservedBelow(mounts []Mount, v, at string) bool {
	for d := range virtualDirs(mounts) {
		if len(d) > len(at) && (v == d || strings.HasPrefix(v, d+"/")) {
			return true
		}
	}
	return false
}

// isVirtualDir reports whether v is a directory made only of mount points.
func isVirtualDir(mounts []Mount, v string) bool { return virtualDirs(mounts)[v] }

// children lists the mount points and virtual directories directly below
// a directory, so `ls` shows them.
func children(mounts []Mount, dir string) []string {
	var out []string
	for _, m := range mounts {
		if m.At == "" {
			continue
		}
		rest := m.At
		if dir != "" {
			if !strings.HasPrefix(m.At, dir+"/") {
				continue
			}
			rest = strings.TrimPrefix(m.At, dir+"/")
		}
		name, deeper, _ := strings.Cut(rest, "/")
		if deeper != "" {
			out = append(out, name+"/")
			continue
		}
		if st, err := os.Stat(m.Host); err == nil && st.IsDir() {
			name += "/"
		}
		out = append(out, name)
	}
	return out
}

// walk visits the files below a resolved directory within its mount, in
// lexical order, skipping .git and not following symlinks. fn gets the
// virtual path and the host path.
func walk(r resolved, limit int, fn func(vpath, host string) bool) {
	n := 0
	_ = filepath.WalkDir(r.host, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(r.host, p)
		v := path.Join(r.vpath, filepath.ToSlash(rel))
		n++
		if (limit > 0 && n > limit) || !fn(v, p) {
			return filepath.SkipAll
		}
		return nil
	})
}

// globMatch matches a slash-separated path against a pattern where "**"
// matches any number of segments and other segments use path.Match.
func globMatch(pattern, name string) bool {
	ps := strings.Split(pattern, "/")
	ns := strings.Split(name, "/")
	var m func(i, j int) bool
	m = func(i, j int) bool {
		for i < len(ps) {
			if ps[i] == "**" {
				for k := j; k <= len(ns); k++ {
					if m(i+1, k) {
						return true
					}
				}
				return false
			}
			if j >= len(ns) {
				return false
			}
			if ok, _ := path.Match(ps[i], ns[j]); !ok {
				return false
			}
			i, j = i+1, j+1
		}
		return j == len(ns)
	}
	return m(0, 0)
}

func sortedNames(ents []os.DirEntry, extra []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range extra {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
