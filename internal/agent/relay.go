package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
)

// CapabilityRemote4Relay is advertised in /healthz capabilities by agents
// that can relay a `driverforge debug` session to its stub over the LAN
// (GET /remote4, an upgraded connection carrying JSON lines both ways).
const CapabilityRemote4Relay = "remote4-relay"

// OpenRemote4Relay upgrades a connection to the deploy server into a debug
// session's LAN relay to the stub installed as filename. session and key are
// the debug session's id and the relay key baked into its stub. The returned
// connection carries newline-delimited JSON in both directions until closed;
// it outlives ctx once open.
func (c *Client) OpenRemote4Relay(ctx context.Context, filename, session, key string) (io.ReadWriteCloser, error) {
	req, err := c.authed(ctx, http.MethodGet, c.baseURL+"/remote4", filename, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "remote4")
	req.Header.Set("X-Remote4-Session", session)
	req.Header.Set("X-Remote4-Key", key)

	resp, err := c.streamingHTTP().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, parseErrorEnvelope(resp.StatusCode, data)
	}
	conn, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return nil, errors.New("agent: relay upgrade returned a read-only body")
	}
	return conn, nil
}
