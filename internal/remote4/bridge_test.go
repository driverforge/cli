package remote4

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRuntime swaps the interpreter for a shell script that plays a scripted
// runtime: it prints `ready`, then answers each stdin line from a canned
// response table, so the bridge is exercised without Lua.
func fakeRuntime(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := newRuntimeCommand
	newRuntimeCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", path)
	}
	t.Cleanup(func() { newRuntimeCommand = prev })
}

func startFake(t *testing.T, script string) *Runtime {
	t.Helper()
	fakeRuntime(t, script)
	t.Setenv("DRIVERFORGE_LUA", "sh")
	rt, err := StartRuntime(context.Background(), RuntimeOptions{SrcDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(time.Second) })
	return rt
}

func collect(t *testing.T, events <-chan Event, want EventKind, timeout time.Duration) Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatalf("events closed before %v arrived", want)
			}
			if e.Kind == want {
				return e
			}
		case <-deadline:
			t.Fatalf("no %v event within %s", want, timeout)
		}
	}
}

func TestRuntimeDecodesLinesAndPassesRawOutput(t *testing.T) {
	rt := startFake(t, `
echo '{"v":1,"type":"compiled","entry":"driver.lua"}'
echo 'stray interpreter output'
read line
echo "{\"v\":1,\"type\":\"print\",\"text\":\"got: $line\"}"
`)
	first := <-rt.Messages()
	if first.Type != "compiled" || first.Entry != "driver.lua" {
		t.Fatalf("unexpected first message %+v", first)
	}
	raw := <-rt.Messages()
	if raw.Type != "raw" || raw.Text != "stray interpreter output" {
		t.Fatalf("unexpected raw message %+v", raw)
	}
	if err := rt.Send(Message{Type: "reload"}); err != nil {
		t.Fatal(err)
	}
	echo := <-rt.Messages()
	if !strings.Contains(echo.Text, `"type":"reload"`) {
		t.Fatalf("runtime did not receive the sent line: %+v", echo)
	}
}

func TestBridgeRelaysBothDirections(t *testing.T) {
	// The fake: on a callback, issue a c4_call, wait for its response, then
	// answer the callback.
	rt := startFake(t, `
echo '{"v":1,"type":"compiled","entry":"driver.lua"}'
while read line; do
  case "$line" in
    *'"type":"load"'*)
      echo '{"v":1,"type":"ready","entry":"driver.lua","handlers":["ReceivedFromProxy"]}' ;;
    *'"type":"callback"'*)
      echo '{"v":1,"type":"c4_call","id":1,"method":"SendToProxy","args":[5001,"ON"],"argc":2}' ;;
    *'"type":"c4_response"'*)
      echo '{"v":1,"type":"print","text":"result received"}'
      echo '{"v":1,"type":"callback_response","id":7,"name":"ReceivedFromProxy","ok":true,"handled":true}' ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`)
	broker := NewMemoryBroker()
	b := NewBridge(rt, broker, "user-1", "sess-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx) }()

	collect(t, b.Events(), EventCompiled, 2*time.Second)

	broker.Deliver(Topic("user-1", "sess-1", TopicStatus), []byte(`{"v":1,"type":"online","driver":"x.c4z"}`))
	if e := collect(t, b.Events(), EventStubOnline, time.Second); e.Message.Driver != "x.c4z" {
		t.Errorf("online event lost the driver name: %+v", e.Message)
	}
	if err := b.Load(); err != nil {
		t.Fatal(err)
	}
	if e := collect(t, b.Events(), EventReady, 2*time.Second); len(e.Message.Handlers) != 1 {
		t.Errorf("ready lost the handler list: %+v", e.Message)
	}

	broker.Deliver(Topic("user-1", "sess-1", TopicCallback), []byte(`{"v":1,"id":7,"name":"ReceivedFromProxy","args":[5001,"ON",{}],"argc":3}`))
	call := collect(t, b.Events(), EventC4Call, 2*time.Second)
	if call.Message.Method != "SendToProxy" {
		t.Fatalf("c4_call not relayed: %+v", call.Message)
	}
	pub := lastPublished(broker, Topic("user-1", "sess-1", TopicC4Call))
	if pub == nil {
		t.Fatal("c4_call not published to the broker")
	}
	var onWire map[string]any
	_ = json.Unmarshal(pub, &onWire)
	if _, has := onWire["type"]; has {
		t.Error("broker envelopes must not carry the stdio type field")
	}
	if onWire["method"] != "SendToProxy" || onWire["v"] != float64(ProtocolVersion) {
		t.Errorf("unexpected wire message %s", pub)
	}

	broker.Deliver(Topic("user-1", "sess-1", TopicC4Response), []byte(`{"v":1,"id":1,"ok":true,"results":[true],"argc":1}`))
	if e := collect(t, b.Events(), EventPrint, 2*time.Second); e.Message.Text != "result received" {
		t.Errorf("print not relayed: %+v", e.Message)
	}
	collect(t, b.Events(), EventCallbackResult, 2*time.Second)
	if lastPublished(broker, Topic("user-1", "sess-1", TopicCallbackResponse)) == nil {
		t.Error("callback_response not published")
	}

	if err := b.SendToken("jwt-2"); err != nil {
		t.Fatal(err)
	}
	if err := b.End(); err != nil {
		t.Fatal(err)
	}
	if err := b.End(); err != nil {
		t.Fatal("End must be idempotent")
	}
	var controls []string
	for _, p := range broker.Published {
		if p.Topic == Topic("user-1", "sess-1", TopicControl) {
			controls = append(controls, string(p.Payload))
		}
	}
	if len(controls) != 2 || !strings.Contains(controls[0], `"token":"jwt-2"`) || !strings.Contains(controls[1], `"type":"end"`) {
		t.Errorf("unexpected control messages %v", controls)
	}
}

func TestBridgeReportsRuntimeExit(t *testing.T) {
	rt := startFake(t, `echo '{"v":1,"type":"fatal","error":"driver.lua:1: boom"}'; exit 1`)
	b := NewBridge(rt, NewMemoryBroker(), "u", "s")
	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background()) }()
	if e := collect(t, b.Events(), EventFatal, 2*time.Second); !strings.Contains(e.Message.Error, "boom") {
		t.Errorf("fatal not relayed: %+v", e.Message)
	}
	collect(t, b.Events(), EventRuntimeExited, 2*time.Second)
	select {
	case err := <-done:
		if err == nil {
			t.Error("Run should return an error when the runtime exits")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after runtime exit")
	}
}

func lastPublished(b *MemoryBroker, topic string) []byte {
	for i := len(b.Published) - 1; i >= 0; i-- {
		if b.Published[i].Topic == topic {
			return b.Published[i].Payload
		}
	}
	return nil
}

func TestTopicAndSummary(t *testing.T) {
	if got := Topic("u", "s", TopicC4Call); got != "remote4/u/s/c4_call" {
		t.Errorf("Topic = %q", got)
	}
	ref := 3
	fn := 9
	cases := map[string]Message{
		`ReceivedFromProxy(5001,"ON")`: {Name: "ReceivedFromProxy", Args: json.RawMessage(`[5001,"ON"]`)},
		`C4:SendToProxy()`:             {Method: "SendToProxy", Args: json.RawMessage(`[]`)},
		`#3:Cancel()`:                  {Method: "Cancel", Ref: &ref},
		`callback #9(1)`:               {Name: "__fn", Fn: &fn, Args: json.RawMessage(`[1]`)},
	}
	for want, m := range cases {
		if got := m.Summary(); got != want {
			t.Errorf("Summary() = %q, want %q", got, want)
		}
	}
}

func TestWatchReportsChanges(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "driver.lua")
	if err := os.WriteFile(file, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan []string, 4)
	go Watch(ctx, dir, 20*time.Millisecond, func(c []string) { changes <- c })
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(file, []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-changes:
		if len(c) != 1 || c[0] != "driver.lua" {
			t.Errorf("changed = %v, want [driver.lua]", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no change reported")
	}
}
