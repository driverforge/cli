package cli

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/driverforgeapi"
)

func TestReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	ctx := context.Background()
	if !reachable(ctx, ln.Addr().String(), time.Second) {
		t.Error("open listener should be reachable")
	}

	// A port nobody is listening on should fail fast.
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := closed.Addr().String()
	closed.Close()
	if reachable(ctx, addr, 500*time.Millisecond) {
		t.Error("closed port should not be reachable")
	}
}

func TestProbeReachabilityUsesAgentPortOverride(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	t.Setenv("DRIVERFORGE_AGENT_PORT", port)

	var got []string
	devices := []driverforgeapi.Device{{IP: "127.0.0.1", DeviceID: "000FFF9BB74C"}}
	probeReachability(context.Background(), devices, func(mac string) { got = append(got, mac) })
	if len(got) != 1 {
		t.Errorf("reachable = %v, want the controller listening on DRIVERFORGE_AGENT_PORT", got)
	}
}
