package dualsense

import "testing"

func TestEdgeDoesNotRenameRegularDualSense(t *testing.T) {
	regular, err := New(nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := NewEdge(nil); err != nil {
		t.Fatalf("NewEdge returned error: %v", err)
	}

	if got := regular.GetDescriptor().Strings[2]; got == "DualSense Edge Wireless Controller" {
		t.Fatalf("creating an Edge renamed an existing DualSense to %q", got)
	}
	if got := defaultDescriptor.Strings[2]; got == "DualSense Edge Wireless Controller" {
		t.Fatalf("creating an Edge renamed the default descriptor to %q", got)
	}
}

func TestVariantsReportTheirRegistryType(t *testing.T) {
	regular, _ := New(nil)
	edge, _ := NewEdge(nil)
	if got := regular.DeviceType(); got != "dualsense" {
		t.Fatalf("DualSense DeviceType = %q", got)
	}
	if got := edge.DeviceType(); got != "dualsenseedge" {
		t.Fatalf("Edge DeviceType = %q", got)
	}
}

func TestNilInputStateResetsToNeutral(t *testing.T) {
	d, _ := New(nil)
	d.UpdateInputState(nil)
	if d.inputState == nil {
		t.Fatal("nil input state was stored")
	}
}

func TestAnalogTriggersAssertDigitalTriggerBits(t *testing.T) {
	d, _ := New(nil)
	s := NewInputState()
	s.L2 = 1
	s.R2 = 200
	b := d.buildUSBInputReport(s, d.metaState)
	want := uint8((ButtonL2 | ButtonR2) >> 8)
	if b[9]&want != want {
		t.Fatalf("byte 9 = %08b, want L2 and R2 bits %08b set", b[9], want)
	}
}
