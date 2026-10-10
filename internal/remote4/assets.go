// Package remote4 implements the laptop side of the Remote4 live-debug proxy:
// bundling and deploying the stub driver, the broker connection, the local
// Lua runtime, and the bridge that relays messages between them. The wire
// contract is platform-control4/docs/remote4-protocol.md.
package remote4

import "embed"

// assets are the Lua programs and the broker CA, vendored byte-for-byte from
// platform-control4 by scripts/vendor-remote4.sh so a released binary carries
// exactly the stub it was tested with.
//
//go:embed assets/json.lua assets/stub.lua assets/runtime.lua assets/broker-ca.pem assets/callbacks.txt
var assets embed.FS

func asset(name string) []byte {
	b, err := assets.ReadFile("assets/" + name)
	if err != nil {
		panic("remote4: missing embedded asset " + name + ": " + err.Error())
	}
	return b
}

// BrokerCA is the PEM bundle for the Anvil broker's private root.
func BrokerCA() []byte { return asset("broker-ca.pem") }
