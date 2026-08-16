package agent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDeployStreamEvents(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"state":"staged","step":"driver written to the controller","terminal":false}
{"state":"reinstalling","step":"director reinstalling the driver","terminal":false}
{"state":"restarting","step":"driver restarting","terminal":false}
{"state":"confirmed","step":"driver restarted","version":"1.2.3","terminal":true}
`)
	})
	var got []DeployEvent
	res, terminal, err := c.DeployStream(context.Background(), "x.c4z", "h", "", strings.NewReader("c4z"), true, func(e DeployEvent) {
		got = append(got, e)
	})
	if err != nil {
		t.Fatalf("DeployStream: %v", err)
	}
	if len(got) != 4 || got[0].State != StateStaged || got[2].State != StateRestarting {
		t.Errorf("events out of order: %+v", got)
	}
	if terminal.State != StateConfirmed || !terminal.Terminal || terminal.Version != "1.2.3" {
		t.Errorf("terminal = %+v", terminal)
	}
	if !res.OK {
		t.Errorf("res = %+v", res)
	}
}

func TestDeployStreamLegacySingleObject(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"filename":"x.c4z","structuralHash":"sha256:legacy"}`)
	})
	var got []DeployEvent
	res, terminal, err := c.DeployStream(context.Background(), "x.c4z", "h", "", strings.NewReader("c4z"), false, func(e DeployEvent) {
		got = append(got, e)
	})
	if err != nil {
		t.Fatalf("legacy DeployStream: %v", err)
	}
	if len(got) != 2 || got[0].State != StateStaged || got[1].State != StateConfirmed || !got[1].Terminal {
		t.Errorf("legacy should synthesize staged→confirmed: %+v", got)
	}
	if res.StructuralHash != "sha256:legacy" {
		t.Errorf("legacy hash lost: %+v", res)
	}
	_ = terminal
}

func TestDeployStreamFailedTerminal(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"state":"failed","step":"driver failed to load","error":"init: index nil","terminal":true}`+"\n")
	})
	res, terminal, err := c.DeployStream(context.Background(), "x.c4z", "h", "", strings.NewReader("c4z"), true, nil)
	if err != nil {
		t.Fatalf("a terminal failed event is not a transport error: %v", err)
	}
	if terminal.State != StateFailed || terminal.Error != "init: index nil" {
		t.Errorf("terminal = %+v", terminal)
	}
	if res.OK {
		t.Error("a failed deploy must not be OK")
	}
}

func TestDeployStreamUnconfirmedAndSelfDeploy(t *testing.T) {
	stubLogin(t)
	unc := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"state":"unconfirmed","step":"no ready signal","hint":"older SDK?","terminal":true}`+"\n")
	})
	_, terminal, err := unc.DeployStream(context.Background(), "x.c4z", "h", "", strings.NewReader("c4z"), true, nil)
	if err != nil || terminal.State != StateUnconfirmed || terminal.Hint != "older SDK?" {
		t.Errorf("unconfirmed: terminal=%+v err=%v", terminal, err)
	}

	self := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"state":"restarting","reason":"self_deploy","terminal":true}`+"\n")
	})
	_, terminal, err = self.DeployStream(context.Background(), "x.c4z", "h", "", strings.NewReader("c4z"), true, nil)
	if err != nil || terminal.Reason != ReasonSelfDeploy || !terminal.Terminal {
		t.Errorf("self-deploy: terminal=%+v err=%v", terminal, err)
	}
}

func TestDeployStreamErrorEnvelope(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"needs_deploy","message":"x"}`)
	})
	_, _, err := c.DeployStream(context.Background(), "x.c4z", "h", "", strings.NewReader("c4z"), true, nil)
	if Code(err) != CodeNeedsDeploy {
		t.Errorf("Code = %q, want needs_deploy (err=%v)", Code(err), err)
	}
}

func TestDeployStreamTruncated(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"state":"staged","step":"x","terminal":false}`) // no terminal event
	})
	var n int
	_, _, err := c.DeployStream(context.Background(), "x.c4z", "h", "", strings.NewReader("c4z"), true, func(DeployEvent) { n++ })
	if err == nil {
		t.Error("a stream with no terminal event should error")
	}
	if n != 1 {
		t.Errorf("the received event should still be delivered, got %d", n)
	}
}

func TestDeployStreamMalformed(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "{\"state\":\"staged\",\"step\":\"x\",\"terminal\":false}\nnot json\n")
	})
	var n int
	_, _, err := c.DeployStream(context.Background(), "x.c4z", "h", "", strings.NewReader("c4z"), true, func(DeployEvent) { n++ })
	if err == nil {
		t.Error("a malformed line should error")
	}
	if n != 1 {
		t.Errorf("the first valid event should be delivered, got %d", n)
	}
}

func TestDeployStreamChunked(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		fl, _ := w.(http.Flusher)
		io.WriteString(w, `{"state":"staged",`) // split an object mid-write
		if fl != nil {
			fl.Flush()
		}
		io.WriteString(w, `"step":"x","terminal":false}`+"\n"+`{"state":"confirmed","step":"done","terminal":true}`)
	})
	var n int
	_, terminal, err := c.DeployStream(context.Background(), "x.c4z", "h", "", strings.NewReader("c4z"), true, func(DeployEvent) { n++ })
	if err != nil {
		t.Fatalf("chunked: %v", err)
	}
	if n != 2 || terminal.State != StateConfirmed {
		t.Errorf("chunked: n=%d terminal=%+v", n, terminal)
	}
}
