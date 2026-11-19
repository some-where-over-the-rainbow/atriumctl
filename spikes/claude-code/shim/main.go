// Command atriumctl-shim is the harness container's entrypoint. The container has no
// network (--network none); the shim bridges two loopback ports to Unix
// sockets mounted from the host, loads the token from a mounted file and runs
// claude with the given arguments.
//
//	127.0.0.1:3128 -> /run/atriumctl/proxy.sock   egress proxy (allowlist)
//	127.0.0.1:8080 -> /run/atriumctl/kernel.sock  tool gateway and telemetry
//
// `atriumctl-shim probe` instead tries to reach the internet directly and exits 0
// only if every attempt fails.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var bridges = map[string]string{
	"127.0.0.1:3128": "/run/atriumctl/proxy.sock",
	"127.0.0.1:8080": "/run/atriumctl/kernel.sock",
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "probe" {
		os.Exit(probe())
	}
	for addr, sock := range bridges {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "shim:", err)
			os.Exit(70)
		}
		go serve(ln, sock)
	}

	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ATRIUMCTL_") {
			env = append(env, kv)
		}
	}
	if f := os.Getenv("ATRIUMCTL_TOKEN_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "shim: token:", err)
			os.Exit(71)
		}
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+strings.TrimSpace(string(b)))
	}
	env = append(env,
		"HTTPS_PROXY=http://127.0.0.1:3128", "HTTP_PROXY=http://127.0.0.1:3128",
		"NO_PROXY=127.0.0.1,localhost")

	cmd := exec.Command("/usr/local/bin/claude", os.Args[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "shim:", err)
		os.Exit(72)
	}
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for s := range sig {
			_ = cmd.Process.Signal(s)
		}
	}()
	err := cmd.Wait()
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			os.Exit(128 + int(ws.Signal()))
		}
		os.Exit(ee.ExitCode())
	}
	if err != nil {
		os.Exit(73)
	}
}

func serve(ln net.Listener, sock string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			u, err := net.Dial("unix", sock)
			if err != nil {
				return
			}
			defer u.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(u, c); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, u); done <- struct{}{} }()
			<-done
		}()
	}
}

func probe() int {
	failures := 0
	try := func(what string, f func() error) {
		err := f()
		if err == nil {
			fmt.Printf("REACHED %s\n", what)
			return
		}
		failures++
		fmt.Printf("blocked %s: %v\n", what, err)
	}
	dial := func(addr string) func() error {
		return func() error {
			c, err := net.DialTimeout("tcp", addr, 3*time.Second)
			if err == nil {
				c.Close()
			}
			return err
		}
	}
	try("tcp 1.1.1.1:443", dial("1.1.1.1:443"))
	try("tcp 160.79.104.10:443 (api.anthropic.com)", dial("160.79.104.10:443"))
	try("dns api.anthropic.com", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := net.DefaultResolver.LookupHost(ctx, "api.anthropic.com")
		return err
	})
	if failures == 3 {
		return 0
	}
	return 1
}
