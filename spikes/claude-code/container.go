package main

// Container mode: claude runs in a rootless Podman container with no network.
// The shim inside bridges loopback ports to the host's proxy and kernel sockets.

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type Container struct {
	Image     string
	RunDir    string // host dir holding proxy.sock and kernel.sock
	TokenFile string // host file holding the long-lived OAuth token
	ConfigDir string // host dir mounted read-only as CLAUDE_CONFIG_DIR
}

const (
	ctrKernelURL = "http://127.0.0.1:8080"
)

func (c *Container) runArgs(name string, extraEnv, claudeArgs []string) []string {
	a := []string{
		"run", "--rm", "-i", "--name", name,
		"--network", "none",
		"--read-only",
		"--userns", "keep-id",
		"--cap-drop", "all",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "512",
		"--tmpfs", "/home/agent:rw,mode=1777",
		"--tmpfs", "/work:rw,mode=1777",
		"--tmpfs", "/tmp:rw,mode=1777",
		"--workdir", "/work",
		"-v", c.RunDir + ":/run/atriumctl",
		"-v", c.ConfigDir + ":/config:ro",
		"-v", c.TokenFile + ":/run/secrets/claude-token:ro",
		"-e", "HOME=/home/agent",
		"-e", "LANG=C.UTF-8",
		"-e", "CLAUDE_CONFIG_DIR=/config",
		"-e", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"-e", "ATRIUMCTL_TOKEN_FILE=/run/secrets/claude-token",
	}
	for _, kv := range extraEnv {
		a = append(a, "-e", kv)
	}
	a = append(a, c.Image)
	return append(a, claudeArgs...)
}

func containerExists(name string) bool {
	out, _ := exec.Command("podman", "ps", "-a", "-q", "--filter", "name=^"+name+"$").Output()
	return strings.TrimSpace(string(out)) != ""
}

// cgroupWatch finds a container's cgroup once and reads memory.peak.
type cgroupWatch struct{ path string }

func (w *cgroupWatch) peakKB(name string) int64 {
	if w.path == "" {
		out, err := exec.Command("podman", "inspect", "--format", "{{.State.CgroupPath}}", name).Output()
		p := strings.TrimSpace(string(out))
		if err != nil || p == "" {
			return 0
		}
		w.path = "/sys/fs/cgroup" + p
	}
	for _, f := range []string{"memory.peak", "memory.current"} {
		b, err := os.ReadFile(w.path + "/" + f)
		if err == nil {
			v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			return v / 1024
		}
	}
	return 0
}
