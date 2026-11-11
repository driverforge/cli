package agent

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"testing"
)

func TestOpenRemote4RelayUpgradesAndCarriesLines(t *testing.T) {
	stubLogin(t)
	var got http.Header
	c := fakeAgent(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: remote4\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		rw.WriteString("echo " + line)
		rw.Flush()
	})

	conn, err := c.OpenRemote4Relay(context.Background(), "acme.c4z", "sess-1", "abcd")
	if err != nil {
		t.Fatalf("OpenRemote4Relay: %v", err)
	}
	defer conn.Close()
	if hs := relayHeaders(got); hs != "Bearer tok|acme.c4z|remote4|sess-1|abcd" {
		t.Errorf("headers = %s", hs)
	}
	if _, err := io.WriteString(conn, `{"topic":"ping"}`+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != `echo {"topic":"ping"}`+"\n" {
		t.Errorf("read %q, %v", line, err)
	}
}

func relayHeaders(h http.Header) string {
	return h.Get("Authorization") + "|" + h.Get("X-Driver-Filename") + "|" + h.Get("Upgrade") + "|" +
		h.Get("X-Remote4-Session") + "|" + h.Get("X-Remote4-Key")
}

func TestOpenRemote4RelayMapsAnErrorEnvelope(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"ok":false,"error":"multiple_instances","message":"two"}`)
	})
	_, err := c.OpenRemote4Relay(context.Background(), "acme.c4z", "s", "k")
	if Code(err) != "multiple_instances" {
		t.Errorf("err = %v (code %q)", err, Code(err))
	}
}
