package executor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// HostRunner runs claude as a host process with a minimal environment.
// For development and tests; slice 1 runs agents in containers.
type HostRunner struct {
	Binary    string // default "claude"
	BaseURL   string // where the kernel's HTTP server listens, e.g. http://127.0.0.1:41000
	TokenFile string // optional: long-lived token; otherwise the logged-in credentials
	ConfigDir string // optional CLAUDE_CONFIG_DIR
	Dir       string // working directory; an empty directory is best
}

func (h *HostRunner) Command(s *Spec, args []string) (*exec.Cmd, string, error) {
	bin := h.Binary
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = h.Dir
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "USER=" + os.Getenv("USER"),
		"LANG=C.UTF-8", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	if h.TokenFile != "" {
		b, err := os.ReadFile(h.TokenFile)
		if err != nil {
			return nil, "", err
		}
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+strings.TrimSpace(string(b)))
	}
	if h.ConfigDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+h.ConfigDir)
	}
	cmd.Env = append(env, s.Env...)
	return cmd, "", nil
}

func (h *HostRunner) GatewayURL(path string) string { return h.BaseURL + path }
func (h *HostRunner) TelemetryEndpoint() string     { return h.BaseURL }
func (h *HostRunner) Gone(string) bool              { return true }

// ContainerRunner runs claude in a rootless Podman harness container with no
// network. The shim inside bridges loopback ports to the host's sockets:
// 127.0.0.1:3128 to the egress proxy, 127.0.0.1:8080 to the kernel (gateway
// and telemetry). The token is a read-only file the shim loads; the config
// directory is an empty read-only mount (docs/spike-claude-code.md §3).
type ContainerRunner struct {
	Image     string // e.g. atriumctl-harness:2.1.327
	RunDir    string // host dir with proxy.sock and kernel.sock
	TokenFile string
	ConfigDir string // empty directory, mounted read-only
	Podman    string // default "podman"
	Runtime   string // optional OCI runtime, e.g. crun
}

const containerKernel = "http://127.0.0.1:8080"

func (c *ContainerRunner) podman() string {
	if c.Podman == "" {
		return "podman"
	}
	return c.Podman
}

func (c *ContainerRunner) Command(s *Spec, args []string) (*exec.Cmd, string, error) {
	if c.TokenFile == "" {
		return nil, "", fmt.Errorf("container runner needs a token file")
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	name := "atriumctl-" + strings.NewReplacer(".", "-", "/", "-", "_", "-").Replace(s.AgentID) + "-" + hex.EncodeToString(b)
	a := []string{"run", "--rm", "-i", "--name", name}
	if c.Runtime != "" {
		a = append(a, "--runtime", c.Runtime)
	}
	a = append(a,
		"--network", "none", "--read-only", "--userns", "keep-id",
		"--cap-drop", "all", "--security-opt", "no-new-privileges", "--pids-limit", "512",
		"--tmpfs", "/home/agent:rw,mode=1777", "--tmpfs", "/work:rw,mode=1777", "--tmpfs", "/tmp:rw,mode=1777",
		"--workdir", "/work",
		"-v", c.RunDir+":/run/atriumctl",
		"-v", c.ConfigDir+":/config:ro",
		"-v", c.TokenFile+":/run/secrets/claude-token:ro",
		"-e", "HOME=/home/agent", "-e", "LANG=C.UTF-8", "-e", "CLAUDE_CONFIG_DIR=/config",
		"-e", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "-e", "ATRIUMCTL_TOKEN_FILE=/run/secrets/claude-token",
	)
	for _, kv := range s.Env {
		a = append(a, "-e", kv)
	}
	a = append(a, c.Image)
	a = append(a, args...)
	return exec.Command(c.podman(), a...), name, nil
}

func (c *ContainerRunner) GatewayURL(path string) string { return containerKernel + path }
func (c *ContainerRunner) TelemetryEndpoint() string     { return containerKernel }

func (c *ContainerRunner) Gone(name string) bool {
	out, err := exec.Command(c.podman(), "ps", "-a", "-q", "--filter", "name=^"+name+"$").Output()
	return err == nil && strings.TrimSpace(string(out)) == ""
}
