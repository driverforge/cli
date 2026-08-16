package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestDeviceListModelGoesOnline(t *testing.T) {
	m := NewDeviceListModel("Devices", []DeviceItem{
		{Name: "Living Room", Host: "-", Status: "ACTIVE", MAC: "000fffa13a46"},
		{Name: "Theater", Host: "-", Status: "ACTIVE", MAC: "00aabbccddee"},
	})

	// Initially: rows present, scanning footer, no live IP yet.
	v := m.View().Content
	if !strings.Contains(v, "Living Room") || !strings.Contains(v, "Theater") {
		t.Fatalf("rows missing:\n%s", v)
	}
	if !strings.Contains(v, "scanning") {
		t.Errorf("expected scanning footer while not done:\n%s", v)
	}

	// A matching advertisement flips the row online and refreshes its address.
	m.Update(DeviceOnlineMsg{MAC: "000fffa13a46", IP: "192.168.30.11"})
	v = m.View().Content
	if !strings.Contains(v, "192.168.30.11") {
		t.Errorf("online device should show its live IP:\n%s", v)
	}

	// A non-matching MAC changes nothing about the rows.
	m.Update(DeviceOnlineMsg{MAC: "ffffffffffff", IP: "10.0.0.9"})
	if strings.Contains(m.View().Content, "10.0.0.9") {
		t.Error("unmatched advertisement must not appear")
	}

	// With one device still grey, the scan keeps running.
	if cmd := m.settleIfAllOnline(); cmd != nil || m.done {
		t.Error("should keep scanning while a device is still grey")
	}

	// The second device's reachable probe makes everything green → settle early
	// (no need to wait for the SSDP window) and drop the scanning footer.
	_, cmd := m.Update(DeviceReachableMsg{MAC: "00aabbccddee"})
	if cmd == nil || !m.done {
		t.Error("all devices online should settle the scan early")
	}
	done := m.View().Content
	if strings.Contains(done, "scanning") {
		t.Errorf("scanning footer should be gone once settled:\n%s", done)
	}
	if !strings.Contains(done, "2 of 2 online") {
		t.Errorf("both devices (SSDP + probe) should count as online:\n%s", done)
	}
}

// Column widths and padding use display width, so non-ASCII names keep the
// HOST/STATUS columns aligned.
func TestDeviceListAlignsNonASCII(t *testing.T) {
	m := NewDeviceListModel("Devices", []DeviceItem{
		{Name: "Wohnzimmer Büro", Host: "10.0.0.5", Status: "ACTIVE"},
		{Name: "Hall", Host: "10.0.0.6", Status: "ACTIVE"},
	})
	m.done = true
	lines := strings.Split(strings.TrimRight(m.View().Content, "\n"), "\n")
	// Find the two device rows (they contain the hosts) and assert the STATUS
	// column starts at the same display column in both.
	var statusCols []int
	for _, ln := range lines {
		if strings.Contains(ln, "10.0.0.") {
			statusCols = append(statusCols, lipgloss.Width(ln[:strings.Index(ln, "ACTIVE")]))
		}
	}
	if len(statusCols) != 2 {
		t.Fatalf("expected 2 device rows, got %d", len(statusCols))
	}
	if statusCols[0] != statusCols[1] {
		t.Errorf("STATUS column misaligned across non-ASCII/ASCII names: %v", statusCols)
	}
}
