package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestDevicePickerSelectsDefaultOnEnter(t *testing.T) {
	items := []DeviceItem{
		{ID: "d1", Name: "Living Room", MAC: "aa"},
		{ID: "d2", Name: "Theater", MAC: "bb"},
	}
	m := NewDevicePicker("Select device", items, "d2") // preselect d2

	// Live presence plumbing records SSDP and probe signals.
	m.Update(DeviceOnlineMsg{MAC: "aa", IP: "192.168.1.5"})
	if m.online["aa"] != "192.168.1.5" {
		t.Errorf("online not recorded: %v", m.online)
	}
	m.Update(DeviceReachableMsg{MAC: "bb"})
	if !m.reachable["bb"] {
		t.Error("reachable not recorded")
	}

	// Enter selects the preselected device.
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Aborted() {
		t.Error("enter should not abort")
	}
	if m.Chosen() != "d2" {
		t.Errorf("chosen = %q, want d2", m.Chosen())
	}
}

func TestDevicePickerAborts(t *testing.T) {
	m := NewDevicePicker("x", []DeviceItem{{ID: "d1", Name: "A", MAC: "aa"}}, "")
	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if !m.Aborted() {
		t.Error("q should abort the picker")
	}
	if m.Chosen() != "" {
		t.Errorf("nothing should be chosen on abort, got %q", m.Chosen())
	}
}
