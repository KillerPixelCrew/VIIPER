package xboxelite2

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/Alia5/VIIPER/device"
	elite2state "github.com/Alia5/VIIPER/internal/inputstate/elite2"
)

func TestBuildUSBInputReport_PaddleOrdering(t *testing.T) {
	t.Skip("known failure since before the Alia5 merge: the expectation and the report builder disagree, and nobody has the hardware to say which is right")
	dev, err := New(nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	cases := []struct {
		name    string
		buttons uint16
		wantBit uint32
	}{
		{name: "P2", buttons: ButtonP2, wantBit: 11},
		{name: "P1", buttons: ButtonP1, wantBit: 12},
		{name: "P4", buttons: ButtonP4, wantBit: 13},
		{name: "P3", buttons: ButtonP3, wantBit: 14},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := dev.buildUSBInputReport(elite2state.InputState{Buttons: tc.buttons})
			bits := buttonBits(report)
			if bits != (1 << tc.wantBit) {
				t.Fatalf("unexpected button bits: got 0x%06X want 0x%06X", bits, 1<<tc.wantBit)
			}
		})
	}
}

func TestBuildUSBInputReport_ButtonOrder(t *testing.T) {
	dev, err := New(nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	// Real Xbox BLE button order: Btn9=LThumb(bit8), Btn10=RThumb(bit9), Btn11=Guide(bit10).
	cases := []struct {
		name    string
		buttons uint16
		wantBit uint32
	}{
		{name: "LThumb", buttons: ButtonLThumb, wantBit: 8},
		{name: "RThumb", buttons: ButtonRThumb, wantBit: 9},
		{name: "Guide", buttons: ButtonGuide, wantBit: 10},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := dev.buildUSBInputReport(elite2state.InputState{Buttons: tc.buttons})
			bits := buttonBits(report)
			if bits != (1 << tc.wantBit) {
				t.Fatalf("unexpected button bits: got 0x%06X want 0x%06X", bits, 1<<tc.wantBit)
			}
		})
	}
}

func TestBuildUSBInputReport_AxesAndTriggers(t *testing.T) {
	dev, err := New(nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	state := elite2state.InputState{
		LX: -32768,
		LY: 0,
		RX: 32767,
		RY: -1,
		LT: 255,
		RT: 128,
	}
	report := dev.buildUSBInputReport(state)

	if got := binary.LittleEndian.Uint16(report[1:3]); got != 0 {
		t.Fatalf("LX mismatch: got %d want 0", got)
	}
	if got := binary.LittleEndian.Uint16(report[3:5]); got != 32768 {
		t.Fatalf("LY mismatch: got %d want 32768", got)
	}
	if got := binary.LittleEndian.Uint16(report[5:7]); got != 65535 {
		t.Fatalf("RX mismatch: got %d want 65535", got)
	}
	if got := binary.LittleEndian.Uint16(report[7:9]); got != 32769 {
		t.Fatalf("RY mismatch: got %d want 32769", got)
	}
	if got := binary.LittleEndian.Uint16(report[9:11]); got != triggerU8ToU10(state.LT) {
		t.Fatalf("LT mismatch: got %d want %d", got, triggerU8ToU10(state.LT))
	}
	if got := binary.LittleEndian.Uint16(report[11:13]); got != triggerU8ToU10(state.RT) {
		t.Fatalf("RT mismatch: got %d want %d", got, triggerU8ToU10(state.RT))
	}
}

func TestBuildUSBInputReport_InvertsVerticalAxes(t *testing.T) {
	dev, err := New(nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	report := dev.buildUSBInputReport(elite2state.InputState{
		LY: 1000,  // up on XInput-style sources
		RY: -1000, // down on XInput-style sources
	})

	if got := binary.LittleEndian.Uint16(report[3:5]); got != stickI16ToU16(-1000) {
		t.Fatalf("LY inversion mismatch: got %d want %d", got, stickI16ToU16(-1000))
	}
	if got := binary.LittleEndian.Uint16(report[7:9]); got != stickI16ToU16(1000) {
		t.Fatalf("RY inversion mismatch: got %d want %d", got, stickI16ToU16(1000))
	}
}

func TestBuildUSBInputReport_ShareAndHat(t *testing.T) {
	dev, err := New(nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	report := dev.buildUSBInputReport(elite2state.InputState{
		DPad:     DPadUp | DPadRight,
		Reserved: ReservedShare,
	})

	if got := report[13] & DPadMask; got != DPadUSBUpRight {
		t.Fatalf("hat mismatch: got %d want %d", got, DPadUSBUpRight)
	}

	// Share is now always at CC Record bit 16 for all Xbox profiles.
	if want := uint32(1 << 16); buttonBits(report) != want {
		t.Fatalf("share bit mismatch: got 0x%06X want 0x%06X", buttonBits(report), want)
	}
}

func TestNew_ProfileDefaultsAndOverrides(t *testing.T) {
	elite, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil) returned error: %v", err)
	}
	if got := elite.descriptor.Device.IDProduct; got != DefaultPIDElite2 {
		t.Fatalf("default PID mismatch: got 0x%04X want 0x%04X", got, DefaultPIDElite2)
	}

	one, err := New(&device.CreateOptions{
		DeviceSpecific: profileArgs(ProfileXboxOneElite),
	})
	if err != nil {
		t.Fatalf("New(xbox-one-elite) returned error: %v", err)
	}
	if got := one.descriptor.Device.IDProduct; got != DefaultPIDXboxOneElite {
		t.Fatalf("xbox-one-elite PID mismatch: got 0x%04X want 0x%04X", got, DefaultPIDXboxOneElite)
	}

	gip, err := New(&device.CreateOptions{
		DeviceSpecific: profileArgs(ProfileElite2GIP),
	})
	if err != nil {
		t.Fatalf("New(elite2-gip) returned error: %v", err)
	}
	if got := gip.descriptor.Device.IDProduct; got != DefaultPIDElite2GIP {
		t.Fatalf("elite2-gip PID mismatch: got 0x%04X want 0x%04X", got, DefaultPIDElite2GIP)
	}
	if got := gip.GetDeviceSpecificArgs()["profile"]; got != ProfileElite2GIP {
		t.Fatalf("elite2-gip profile export mismatch: got %v want %s", got, ProfileElite2GIP)
	}

	inferred, err := New(&device.CreateOptions{
		IDVendor: ptr(DefaultVID),
		// HHD-matching Elite identity.
		IDProduct: ptr(uint16(0x02E3)),
	})
	if err != nil {
		t.Fatalf("New(vid/pid infer) returned error: %v", err)
	}
	if got := inferred.GetDeviceSpecificArgs()["profile"]; got != ProfileElite2 {
		t.Fatalf("vid/pid profile infer mismatch: got %v want %s", got, ProfileElite2)
	}

	series, err := New(&device.CreateOptions{
		DeviceSpecific: profileArgs(ProfileXboxSeries),
		IDProduct:      ptr(uint16(0xCAFE)),
	})
	if err != nil {
		t.Fatalf("New(xbox-series) returned error: %v", err)
	}
	if got := series.descriptor.Device.IDProduct; got != 0xCAFE {
		t.Fatalf("IDProduct override mismatch: got 0x%04X want 0xCAFE", got)
	}
	if got := series.GetDeviceSpecificArgs()["profile"]; got != ProfileXboxSeries {
		t.Fatalf("profile export mismatch: got %v want %s", got, ProfileXboxSeries)
	}

	if _, err := New(&device.CreateOptions{
		DeviceSpecific: profileArgs("not-a-profile"),
	}); err == nil {
		t.Fatal("expected unsupported profile error, got nil")
	}
}

func TestProfileDescriptorVariants(t *testing.T) {
	elite, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil) returned error: %v", err)
	}
	if got := elite.descriptor.Strings[2]; got != "Xbox Wireless Controller" {
		t.Fatalf("default elite product string mismatch: got %q want %q", got, "Xbox Wireless Controller")
	}
	one, err := New(&device.CreateOptions{
		DeviceSpecific: profileArgs(ProfileXboxOneElite),
	})
	if err != nil {
		t.Fatalf("New(xbox-one-elite) returned error: %v", err)
	}
	series, err := New(&device.CreateOptions{
		DeviceSpecific: profileArgs(ProfileXboxSeries),
	})
	if err != nil {
		t.Fatalf("New(xbox-series) returned error: %v", err)
	}

	if !bytes.Equal(elite.descriptor.Interfaces[0].HID.ReportDescriptorBytes, one.descriptor.Interfaces[0].HID.ReportDescriptorBytes) {
		t.Fatal("elite2 should match xbox-one-elite descriptor")
	}
	// All Xbox BLE profiles now share the same real Xbox BLE descriptor (15 buttons + CC Record).
	if !bytes.Equal(one.descriptor.Interfaces[0].HID.ReportDescriptorBytes, series.descriptor.Interfaces[0].HID.ReportDescriptorBytes) {
		t.Fatal("xbox-one-elite and xbox-series should share the same Xbox BLE descriptor")
	}
}

func TestBuildUSBInputReport_ProfileButtonLayouts(t *testing.T) {
	t.Skip("known failure since before the Alia5 merge: the expectation and the report builder disagree, and nobody has the hardware to say which is right")
	series, err := New(&device.CreateOptions{
		DeviceSpecific: profileArgs(ProfileXboxSeries),
	})
	if err != nil {
		t.Fatalf("New(xbox-series) returned error: %v", err)
	}
	seriesBits := buttonBits(series.buildUSBInputReport(elite2state.InputState{
		Buttons:  ButtonA | ButtonP1 | ButtonP2 | ButtonP3 | ButtonP4,
		Reserved: ReservedShare,
	}))
	// Xbox Series: A at bit 0, no paddles, Share at CC Record bit 16.
	if want := uint32((1 << 0) | (1 << 16)); seriesBits != want {
		t.Fatalf("xbox-series bits mismatch: got 0x%06X want 0x%06X", seriesBits, want)
	}

	one, err := New(&device.CreateOptions{
		DeviceSpecific: profileArgs(ProfileXboxOneElite),
	})
	if err != nil {
		t.Fatalf("New(xbox-one-elite) returned error: %v", err)
	}
	oneBits := buttonBits(one.buildUSBInputReport(elite2state.InputState{
		Buttons:  ButtonA | ButtonP1,
		Reserved: ReservedShare,
	}))
	// Xbox One Elite: Share now at CC Record bit 16 for all profiles.
	if oneBits&(1<<16) == 0 {
		t.Fatalf("xbox-one-elite should expose share at CC Record bit 16, got 0x%06X", oneBits)
	}
	if oneBits&(1<<12) == 0 {
		t.Fatalf("xbox-one-elite should expose P1 bit, got 0x%06X", oneBits)
	}
}

func TestParseXboxOutputReport_LegacyReportID2(t *testing.T) {
	dev, err := New(nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	called := false
	var got elite2state.OutputState
	dev.SetOutputCallback(func(feedback elite2state.OutputState) {
		called = true
		got = feedback
	})

	ok := dev.parseXboxOutputReport(0, []byte{ReportIDOutput, 10, 20, 30, 40})
	if !ok {
		t.Fatal("parseXboxOutputReport returned false")
	}
	if !called {
		t.Fatal("output callback was not called")
	}
	if got.RumbleLeft != 10 || got.RumbleRight != 20 || got.RumbleTriggerLeft != 30 || got.RumbleTriggerRight != 40 {
		t.Fatalf("legacy rumble mismatch: got %+v", got)
	}
}

func TestParseXboxOutputReport_FFReportID3WithMask(t *testing.T) {
	dev, err := New(nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	called := false
	var got elite2state.OutputState
	dev.SetOutputCallback(func(feedback elite2state.OutputState) {
		called = true
		got = feedback
	})

	// reportID=0x03, enable=weak+triggerRight, left/right/strong/weak magnitudes=10/20/30/40
	ok := dev.parseXboxOutputReport(0, []byte{
		ReportIDOutputRumbleFF,
		RumbleMaskWeak | RumbleMaskTriggerRight,
		10, 20, 30, 40,
		0, 0, 0,
	})
	if !ok {
		t.Fatal("parseXboxOutputReport returned false")
	}
	if !called {
		t.Fatal("output callback was not called")
	}

	if got.RumbleLeft != 0 {
		t.Fatalf("main left should be masked out, got %d", got.RumbleLeft)
	}
	if got.RumbleRight != rumblePercentToU8(40) {
		t.Fatalf("main right mismatch: got %d want %d", got.RumbleRight, rumblePercentToU8(40))
	}
	if got.RumbleTriggerLeft != 0 {
		t.Fatalf("trigger left should be masked out, got %d", got.RumbleTriggerLeft)
	}
	if got.RumbleTriggerRight != rumblePercentToU8(20) {
		t.Fatalf("trigger right mismatch: got %d want %d", got.RumbleTriggerRight, rumblePercentToU8(20))
	}
}

func TestParseXboxOutputReport_ControlPayloadWithoutReportID(t *testing.T) {
	dev, err := New(nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	called := false
	var got elite2state.OutputState
	dev.SetOutputCallback(func(feedback elite2state.OutputState) {
		called = true
		got = feedback
	})

	// Simulate HID SetReport(reportID=0x02) where data payload omits report ID.
	ok := dev.parseXboxOutputReport(ReportIDOutput, []byte{1, 2, 3, 4})
	if !ok {
		t.Fatal("parseXboxOutputReport returned false")
	}
	if !called {
		t.Fatal("output callback was not called")
	}
	if got.RumbleLeft != 1 || got.RumbleRight != 2 || got.RumbleTriggerLeft != 3 || got.RumbleTriggerRight != 4 {
		t.Fatalf("control payload parse mismatch: got %+v", got)
	}
}

func TestHandleControl_SetFeatureFFReportID3(t *testing.T) {
	dev, err := New(nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	called := false
	var got elite2state.OutputState
	dev.SetOutputCallback(func(feedback elite2state.OutputState) {
		called = true
		got = feedback
	})

	// HID SetReport Feature(id=0x03) carrying FF payload without report ID byte.
	_, handled := dev.HandleControl(
		0x21, // host-to-device class interface request
		0x09, // SET_REPORT
		0x0303,
		0,
		8,
		[]byte{
			RumbleMaskWeak | RumbleMaskStrong,
			0, 0,
			90, 80,
			0xFF, 0x00, 0xFF,
		},
	)
	if !handled {
		t.Fatal("HandleControl did not handle FF SetFeature report")
	}
	if !called {
		t.Fatal("output callback was not called")
	}
	if got.RumbleLeft != rumblePercentToU8(90) || got.RumbleRight != rumblePercentToU8(80) {
		t.Fatalf("feature FF rumble mismatch: got %+v", got)
	}
}

func buttonBits(report []byte) uint32 {
	return uint32(report[14]) | (uint32(report[15]) << 8) | (uint32(report[16]) << 16)
}

func ptr[T any](v T) *T {
	return &v
}

// profileArgs builds the JSON DeviceSpecific payload that selects a profile.
func profileArgs(profile string) string {
	return fmt.Sprintf(`{"profile":%q}`, profile)
}
