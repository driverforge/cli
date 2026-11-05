package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/driverforge/cli/internal/discovery"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/ui"
)

// deviceScanWindow is how long `device list` listens for SSDP advertisements
// before settling (Control4 controllers announce periodically, every ~2s).
const deviceScanWindow = 6 * time.Second

// agentPort is the controller's deploy-server (and probe) port.
const agentPort = "9443"

// agentPortInUse is agentPort, or DRIVERFORGE_AGENT_PORT when the agent's
// Deploy Server Port property has been changed.
func agentPortInUse() string {
	if p := os.Getenv("DRIVERFORGE_AGENT_PORT"); p != "" {
		return p
	}
	return agentPort
}

// runDeviceListTUI shows the graph's devices with a live presence dot, fed by two
// signals: SSDP advertisements (same-segment, fresh address) and a direct TCP
// probe of each device's stored address (the cross-VLAN signal SSDP can't give).
func runDeviceListTUI(ctx context.Context, devices []driverforgeapi.Device, currentID string) error {
	m := ui.NewDeviceListModel("Devices", buildDeviceItems(devices, currentID))
	p := tea.NewProgram(m, tea.WithContext(ctx))
	go func() {
		_, _ = discovery.SearchControllers(ctx, deviceScanWindow, discovery.WithController(func(c discovery.Controller) {
			p.Send(ui.DeviceOnlineMsg{MAC: discovery.NormalizeMAC(c.MAC()), IP: c.Host})
		}))
		p.Send(ui.DeviceScanDoneMsg{})
	}()
	go probeReachability(ctx, devices, func(mac string) {
		p.Send(ui.DeviceReachableMsg{MAC: mac})
	})
	if _, err := p.Run(); err != nil {
		return ui.ExpectedIfAborted(err)
	}
	return nil
}

// probeReachability TCP-dials each device's stored address on the agent port and
// reports the ones whose agent answers. Unicast, so it reaches controllers on
// other VLANs that SSDP multicast can't. Probes run concurrently.
func probeReachability(ctx context.Context, devices []driverforgeapi.Device, onReachable func(mac string)) {
	var wg sync.WaitGroup
	for _, d := range devices {
		host, mac := hostOf(d), discovery.NormalizeMAC(d.DeviceID)
		if host == "" || mac == "" {
			continue
		}
		wg.Add(1)
		go func(host, mac string) {
			defer wg.Done()
			if reachable(ctx, net.JoinHostPort(host, agentPortInUse()), 2*time.Second) {
				onReachable(mac)
			}
		}(host, mac)
	}
	wg.Wait()
}

// reachable reports whether a TCP connection to addr succeeds within timeout.
func reachable(ctx context.Context, addr string, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// buildDeviceItems maps graph devices to live-list rows, normalizing the deviceId
// to the bare MAC used to match SSDP hits.
func buildDeviceItems(devices []driverforgeapi.Device, currentID string) []ui.DeviceItem {
	items := make([]ui.DeviceItem, len(devices))
	for i, d := range devices {
		items[i] = ui.DeviceItem{
			ID:      d.ID,
			Name:    d.DisplayName(),
			Host:    dash(hostOf(d)),
			Status:  d.Status,
			MAC:     discovery.NormalizeMAC(d.DeviceID),
			Current: d.ID == currentID,
		}
	}
	return items
}

// pickDevice shows the live two-line picker over active devices, preselecting
// defID, with presence dots fed by the same SSDP + probe signals as the list.
func pickDevice(ctx context.Context, devices []driverforgeapi.Device, defID string) (driverforgeapi.Device, error) {
	m := ui.NewDevicePicker("Select device", buildDeviceItems(devices, defID), defID)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	go func() {
		_, _ = discovery.SearchControllers(ctx, deviceScanWindow, discovery.WithController(func(c discovery.Controller) {
			p.Send(ui.DeviceOnlineMsg{MAC: discovery.NormalizeMAC(c.MAC()), IP: c.Host})
		}))
		p.Send(ui.DeviceScanDoneMsg{})
	}()
	go probeReachability(ctx, devices, func(mac string) {
		p.Send(ui.DeviceReachableMsg{MAC: mac})
	})
	if _, err := p.Run(); err != nil {
		return driverforgeapi.Device{}, ui.ExpectedIfAborted(err)
	}
	if m.Aborted() {
		return driverforgeapi.Device{}, ui.ExpectedIfAborted(tea.ErrInterrupted)
	}
	d := findByDeviceID(devices, m.Chosen())
	if d == nil {
		return driverforgeapi.Device{}, fmt.Errorf("no device selected")
	}
	return *d, nil
}
