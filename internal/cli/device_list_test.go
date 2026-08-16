package cli

import (
	"context"
	"net"
	"testing"
	"time"
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
