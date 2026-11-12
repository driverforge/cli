package cli

import "testing"

// --debug alone connects to the default debugger address; --debug=host:port
// picks another; without the flag no debugger is loaded.
func TestRunDebugFlag(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--debug"}, defaultDebugAddr},
		{[]string{"--debug=10.0.0.5:9000"}, "10.0.0.5:9000"},
	} {
		cmd := newRunCmd()
		if err := cmd.ParseFlags(tc.args); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if got, _ := cmd.Flags().GetString("debug"); got != tc.want {
			t.Errorf("%v: --debug = %q, want %q", tc.args, got, tc.want)
		}
	}
}
