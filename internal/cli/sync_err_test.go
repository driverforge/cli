package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/orgctx"
)

// Structured agent rejections are anticipated operational answers — they must
// render as friendly cards, never crash cards (mirrors friendlyDeployErr).
func TestFriendlySyncErr(t *testing.T) {
	dev := orgctx.Device{Name: "Theatre EA-5"}
	cases := []struct {
		name      string
		err       error
		wantUser  bool
		wantTitle string
	}{
		{"not installed", &agent.Error{Code: agent.CodeNotInstalled, Message: "driver not installed"}, true, "Driver not installed"},
		{"self sync", &agent.Error{Code: agent.CodeSelfSyncForbidden, Message: "cannot sync self"}, true, "Can't sync the agent"},
		{"other agent rejection", &agent.Error{Status: 409, Code: "busy", Message: "another deploy in progress"}, true, "Sync failed"},
		{"wrapped agent rejection", fmt.Errorf("call: %w", &agent.Error{Code: "busy", Message: "busy"}), true, "Sync failed"},
		{"transport error", errors.New("connection reset"), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := friendlySyncErr(dev, "acme.c4z", tc.err)
			var ue *clierr.UserError
			isUser := errors.As(got, &ue)
			if isUser != tc.wantUser {
				t.Fatalf("IsUser = %v, want %v (err=%v)", isUser, tc.wantUser, got)
			}
			if tc.wantUser && ue.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", ue.Title, tc.wantTitle)
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("translated error must wrap the original for errors.Is")
			}
		})
	}
}
