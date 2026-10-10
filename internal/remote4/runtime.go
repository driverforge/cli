package remote4

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RuntimeOptions describes the local Lua process to start.
type RuntimeOptions struct {
	Lua           string // interpreter; "" picks the first of lua5.1, luajit, lua on PATH
	SrcDir        string
	Entry         string // relative to SrcDir; "" means driver.lua
	Configuration string // selects config.<name>.lua, optional
	MobDebug      string // host:port to start mobdebug on, optional
	Stderr        io.Writer
}

// Runtime is a running runtime.lua process speaking JSON lines on stdio.
type Runtime struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	messages chan Message
	done     chan struct{}
	workDir  string

	mu      sync.Mutex
	waitErr error
	closed  bool
}

// newRuntimeCommand is the seam tests use to substitute the interpreter.
var newRuntimeCommand = exec.CommandContext

// FindLua returns the interpreter to use: DRIVERFORGE_LUA, else the first of
// lua5.1, luajit, lua found on PATH.
func FindLua(explicit string) (string, error) {
	if explicit != "" {
		return exec.LookPath(explicit)
	}
	if v := os.Getenv("DRIVERFORGE_LUA"); v != "" {
		return exec.LookPath(v)
	}
	for _, name := range []string{"lua5.1", "luajit", "lua"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no Lua 5.1 interpreter found on PATH (lua5.1, luajit or lua)")
}

// StartRuntime writes runtime.lua and json.lua to a scratch directory and
// starts the interpreter on the project's source. Messages() yields what it
// prints; the caller must drain it.
func StartRuntime(ctx context.Context, o RuntimeOptions) (*Runtime, error) {
	lua, err := FindLua(o.Lua)
	if err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp("", "driverforge-remote4-")
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"runtime.lua", "json.lua"} {
		if err := os.WriteFile(filepath.Join(work, name), asset(name), 0o644); err != nil {
			os.RemoveAll(work)
			return nil, err
		}
	}
	entry := o.Entry
	if entry == "" {
		entry = "driver.lua"
	}
	args := []string{filepath.Join(work, "runtime.lua"), "--src", o.SrcDir, "--entry", entry}
	if o.Configuration != "" {
		args = append(args, "--config", o.Configuration)
	}
	if o.MobDebug != "" {
		args = append(args, "--mobdebug", o.MobDebug)
	}
	cmd := newRuntimeCommand(ctx, lua, args...)
	cmd.Dir = o.SrcDir
	cmd.Stderr = o.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(work)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(work)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		os.RemoveAll(work)
		return nil, fmt.Errorf("starting %s: %w", lua, err)
	}
	r := &Runtime{cmd: cmd, stdin: stdin, messages: make(chan Message, 64), done: make(chan struct{}), workDir: work}
	go r.read(stdout)
	return r, nil
}

func (r *Runtime) read(stdout io.Reader) {
	defer close(r.messages)
	defer close(r.done)
	defer os.RemoveAll(r.workDir)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		trimmed := strings.TrimSpace(string(line))
		if strings.HasPrefix(trimmed, "{") {
			if m, err := Decode(line); err == nil {
				r.messages <- m
				continue
			}
		}
		if trimmed != "" {
			r.messages <- Message{Type: "raw", Text: trimmed}
		}
	}
	err := r.cmd.Wait()
	r.mu.Lock()
	r.waitErr = err
	r.mu.Unlock()
}

// Messages yields decoded runtime output; closed when the process exits.
func (r *Runtime) Messages() <-chan Message { return r.messages }

// Send writes one message to the runtime's stdin.
func (r *Runtime) Send(m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("runtime has exited")
	}
	_, err := r.stdin.Write(append(Encode(m), '\n'))
	return err
}

// Shutdown asks the runtime to exit and waits up to timeout before killing it.
func (r *Runtime) Shutdown(timeout time.Duration) error {
	_ = r.Send(Message{Type: "shutdown"})
	r.mu.Lock()
	r.closed = true
	_ = r.stdin.Close()
	r.mu.Unlock()
	select {
	case <-r.done:
	case <-time.After(timeout):
		_ = r.cmd.Process.Kill()
		<-r.done
	}
	return r.Err()
}

// Done is closed once the process has exited and its output is drained.
func (r *Runtime) Done() <-chan struct{} { return r.done }

// Err is the process exit error, once Done is closed.
func (r *Runtime) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waitErr
}
