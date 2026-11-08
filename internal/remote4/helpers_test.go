package remote4

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func requireLua(t *testing.T) string {
	t.Helper()
	lua, err := FindLua("")
	if err != nil {
		t.Skip("no lua interpreter on PATH")
	}
	return lua
}

// runStubCheck runs script under Lua against a stub bundled from cfg. The
// script loads the stub with dofile(arg[1]) and asserts; a failed assertion
// fails the test.
func runStubCheck(t *testing.T, cfg StubConfig, script string) {
	t.Helper()
	lua := requireLua(t)
	dir := t.TempDir()
	stub := filepath.Join(dir, "stub.lua")
	if err := os.WriteFile(stub, Bundle(cfg, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	check := filepath.Join(dir, "check.lua")
	if err := os.WriteFile(check, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(lua, check, stub).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// startDriver writes files (slash-separated paths) into a fresh source
// directory, starts the real runtime on it and loads the driver with ctx.
func startDriver(t *testing.T, files map[string]string, ctx json.RawMessage) *Runtime {
	t.Helper()
	requireLua(t)
	src := t.TempDir()
	for name, body := range files {
		path := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rt, err := StartRuntime(context.Background(), RuntimeOptions{SrcDir: src})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(time.Second) })
	if err := rt.Send(Message{Type: "load", Ctx: ctx}); err != nil {
		t.Fatal(err)
	}
	return rt
}

// awaitMessage returns the runtime's next message of type want, skipping
// others. A fatal, an exit or five seconds without one fails the test.
func awaitMessage(t *testing.T, rt *Runtime, want string) Message {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-rt.Messages():
			if !ok {
				t.Fatalf("runtime exited before a %s", want)
			}
			if m.Type == "fatal" {
				t.Fatalf("driver failed: %s", m.Error)
			}
			if m.Type == want {
				return m
			}
		case <-deadline:
			t.Fatalf("no %s within 5s", want)
		}
	}
}

// controlMessageFrom runs a bridge over a fake runtime that prints line, and
// returns the first message the bridge sends the stub on the control topic.
func controlMessageFrom(t *testing.T, line string) string {
	t.Helper()
	rt := startFake(t, "echo '"+line+"'; while read line; do :; done")
	broker := NewMemoryBroker()
	b := NewBridge(rt, broker, "user-1", "sess-1")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = b.Run(ctx) }()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if p := lastPublished(broker, Topic("user-1", "sess-1", TopicControl)); p != nil {
			return string(p)
		}
	}
	t.Fatal("nothing published on the control topic")
	return ""
}
