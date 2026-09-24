package xboxelite2

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/Alia5/VIIPER/device"
	elite2state "github.com/Alia5/VIIPER/internal/inputstate/elite2"
	"github.com/Alia5/VIIPER/usb"
	"github.com/Alia5/VIIPER/usbip"
)

type XboxElite2 struct {
	gate *device.InputGate
	// inputState is stored by value: retaining a caller pointer forced a
	// heap allocation per UpdateInputState call at input rate.
	inputState elite2state.InputState
	stateMu    sync.Mutex
	outputFunc func(elite2state.OutputState)
	descriptor usb.Descriptor
	profile    string
}

type xboxElite2CreateOptions struct {
	Profile *string `json:"profile"`
}

func New(o *device.CreateOptions) (*XboxElite2, error) {
	d := &XboxElite2{
		gate:       device.NewInputGate(),
		descriptor: cloneDescriptor(defaultDescriptor),
		profile:    ProfileElite2,
	}
	d.applyProfileDefaults(ProfileElite2)
	profileExplicit := false

	if o != nil && o.DeviceSpecific != "" {
		var args xboxElite2CreateOptions
		data := []byte(o.DeviceSpecific)
		if err := json.Unmarshal(data, &args); err != nil {
			return nil, fmt.Errorf("invalid JSON payload: %w", err)
		}
		if args.Profile != nil {
			profile, err := parseProfile(*args.Profile)
			if err != nil {
				return nil, err
			}
			d.applyProfileDefaults(profile)
			profileExplicit = true
		}
	}

	if o != nil {
		// Older clients may only expose VID/PID overrides.
		// If no explicit profile was requested, infer profile metadata for known IDs.
		if !profileExplicit {
			vid := d.descriptor.Device.IDVendor
			pid := d.descriptor.Device.IDProduct
			if o.IDVendor != nil {
				vid = *o.IDVendor
			}
			if o.IDProduct != nil {
				pid = *o.IDProduct
			}
			if inferred, ok := profileForIDs(vid, pid); ok {
				d.applyProfileDefaults(inferred)
			}
		}
		if o.IDVendor != nil {
			d.descriptor.Device.IDVendor = *o.IDVendor
		}
		if o.IDProduct != nil {
			d.descriptor.Device.IDProduct = *o.IDProduct
		}
	}

	return d, nil
}

func parseProfile(profile string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "", ProfileElite2, "xboxelite2", "xbox-elite2", "series2":
		return ProfileElite2, nil
	case ProfileElite2GIP, "xboxelite2-gip", "xbox-elite2-gip", "gip", "0x0b00", "0b00":
		return ProfileElite2GIP, nil
	case ProfileXboxOne, "xboxone", "xbox-one-s":
		return ProfileXboxOne, nil
	case ProfileXboxOneElite, "xbox-elite", "xboxoneelite", "xbox-one-elite-1", "elite1":
		return ProfileXboxOneElite, nil
	case ProfileXboxSeries, "xboxseries", "series":
		return ProfileXboxSeries, nil
	default:
		return "", fmt.Errorf(
			"unsupported xboxelite2 profile %q (supported: %q, %q, %q, %q, %q)",
			profile,
			ProfileElite2,
			ProfileElite2GIP,
			ProfileXboxOne,
			ProfileXboxOneElite,
			ProfileXboxSeries,
		)
	}
}

func profileForIDs(vid, pid uint16) (string, bool) {
	switch {
	case vid == DefaultVID && pid == DefaultPIDElite2:
		return ProfileElite2, true
	case vid == DefaultVID && pid == DefaultPIDElite2GIP:
		return ProfileElite2GIP, true
	default:
		return "", false
	}
}

func cloneDescriptor(src usb.Descriptor) usb.Descriptor {
	dst := src

	dst.Interfaces = make([]usb.InterfaceConfig, len(src.Interfaces))
	copy(dst.Interfaces, src.Interfaces)
	for i := range src.Interfaces {
		if src.Interfaces[i].HID != nil {
			hid := *src.Interfaces[i].HID
			hid.Descriptor.Descriptors = append([]usb.HIDSubDescriptor(nil), src.Interfaces[i].HID.Descriptor.Descriptors...)
			hid.ReportDescriptorBytes = append([]byte(nil), src.Interfaces[i].HID.ReportDescriptorBytes...)
			dst.Interfaces[i].HID = &hid
		}
		dst.Interfaces[i].Endpoints = append([]usb.EndpointDescriptor(nil), src.Interfaces[i].Endpoints...)
		dst.Interfaces[i].ClassDescriptors = append([]usb.ClassSpecificDescriptor(nil), src.Interfaces[i].ClassDescriptors...)
	}

	dst.Strings = make(map[uint8]string, len(src.Strings))
	for k, v := range src.Strings {
		dst.Strings[k] = v
	}

	return dst
}

func (x *XboxElite2) applyProfileDefaults(profile string) {
	x.profile = profile
	x.descriptor = cloneDescriptor(defaultDescriptor)

	switch profile {
	case ProfileElite2:
		// PID 0x0B13 (Xbox Wireless Controller Model 1914) — the PID that
		// actually parses on Windows with our descriptor. SDL identifies as
		// "Xbox Wireless Controller" / "Xbox Series X Controller". The
		// "Elite 2" label is unreachable through USBIP HID without matching
		// Microsoft's hardcoded byte layout for PIDs 0x0B05/0x0B00.
		x.descriptor.Device.IDVendor = DefaultVID
		x.descriptor.Device.IDProduct = DefaultPIDElite2
		x.descriptor.Strings[2] = "Xbox Wireless Controller"
		x.descriptor.Strings[3] = "VIIPER-XBOX-1914-01"
		x.descriptor.Interfaces[0].HID.ReportDescriptorBytes = append([]byte(nil), xboxBLEHIDDescriptor...)
	case ProfileElite2GIP:
		x.descriptor.Device.IDVendor = DefaultVID
		x.descriptor.Device.IDProduct = DefaultPIDElite2GIP
		x.descriptor.Strings[2] = "Xbox Wireless Controller"
		x.descriptor.Strings[3] = "VIIPER-XE2GIP-01"
		x.descriptor.Interfaces[0].HID.ReportDescriptorBytes = append([]byte(nil), xboxBLEHIDDescriptor...)
	case ProfileXboxOne:
		x.descriptor.Device.IDVendor = DefaultVID
		x.descriptor.Device.IDProduct = DefaultPIDXboxOne
		x.descriptor.Strings[2] = "Xbox Wireless Controller"
		x.descriptor.Strings[3] = "VIIPER-XBO-01"
		x.descriptor.Interfaces[0].HID.ReportDescriptorBytes = append([]byte(nil), xboxBLEHIDDescriptor...)
	case ProfileXboxOneElite:
		x.descriptor.Device.IDVendor = DefaultVID
		x.descriptor.Device.IDProduct = DefaultPIDXboxOneElite
		x.descriptor.Strings[2] = "Xbox One Elite Controller"
		x.descriptor.Strings[3] = "VIIPER-XE1-01"
		x.descriptor.Interfaces[0].HID.ReportDescriptorBytes = append([]byte(nil), xboxBLEHIDDescriptor...)
	case ProfileXboxSeries:
		x.descriptor.Device.IDVendor = DefaultVID
		x.descriptor.Device.IDProduct = DefaultPIDXboxSeries
		x.descriptor.Strings[2] = "Xbox Wireless Controller"
		x.descriptor.Strings[3] = "VIIPER-XSX-01"
		x.descriptor.Interfaces[0].HID.ReportDescriptorBytes = append([]byte(nil), xboxBLEHIDDescriptor...)
	default:
		x.descriptor.Device.IDVendor = DefaultVID
		x.descriptor.Device.IDProduct = DefaultPIDElite2
		x.descriptor.Strings[2] = "Xbox Wireless Controller"
		x.descriptor.Strings[3] = "VIIPER-XBOX-1914-01"
		x.descriptor.Interfaces[0].HID.ReportDescriptorBytes = append([]byte(nil), xboxBLEHIDDescriptor...)
	}
}

func (x *XboxElite2) buttonLayout() (includePaddles bool) {
	// 2026-05-16: paddle slots removed from the HID descriptor across all
	// xbox profiles to restore xinputhid.sys binding (Microsoft's HID-to-
	// XInput translator only binds when the descriptor matches the Xbox
	// Wireless Controller Model 1914 spec — 12 buttons, no paddles).
	// Wire format still carries ButtonP1..P4 in InputState.Buttons for any
	// future non-xinputhid consumer, but they are not written into the
	// outgoing HID report. See const.go button block comment for context.
	return false
}

func (x *XboxElite2) SetOutputCallback(f func(elite2state.OutputState)) {
	x.outputFunc = f
}

func (x *XboxElite2) UpdateInputState(state *elite2state.InputState) {
	x.stateMu.Lock()
	if state == nil {
		x.inputState = elite2state.InputState{}
	} else {
		x.inputState = *state
	}
	x.stateMu.Unlock()
	x.gate.Signal()
}

func (x *XboxElite2) HandleTransfer(ctx context.Context, ep uint32, dir uint32, out []byte) []byte {
	if dir == usbip.DirIn {
		switch ep {
		case 1: // 0x81 - main input reports
			if device.GateCancelled == x.gate.Wait(ctx) {
				return nil
			}
			x.stateMu.Lock()
			st := x.inputState
			x.stateMu.Unlock()

			return x.buildUSBInputReport(st)
		default:
			return nil
		}
	}

	if dir == usbip.DirOut && ep == 1 {
		x.parseXboxOutputReport(0, out)
	}

	return nil
}

func xboxOutputReportID(reportID uint8) bool {
	return reportID == ReportIDOutput || reportID == ReportIDOutputRumbleFF
}

func xboxOutputPayloadSize(reportID uint8) int {
	switch reportID {
	case ReportIDOutput:
		return OutputReportSize - 1
	case ReportIDOutputRumbleFF:
		return OutputReportSizeRumbleFF - 1
	default:
		return 0
	}
}

func normalizeXboxOutputReport(setupReportID uint8, data []byte) (reportID uint8, payload []byte, ok bool) {
	// HID SetReport over control provides the report ID in wValue.
	// Prefer that ID to avoid ambiguity with payload byte 0 (e.g. FF enable=0x03).
	if xboxOutputReportID(setupReportID) {
		expected := xboxOutputPayloadSize(setupReportID)
		if len(data) >= expected+2 && data[0] == 0x00 && data[1] == setupReportID {
			return setupReportID, data[2:], true
		}
		if len(data) >= expected+1 && data[0] == setupReportID {
			return setupReportID, data[1:], true
		}
		return setupReportID, data, true
	}

	// Some hosts prepend a zero byte before report ID on interrupt OUT.
	if len(data) >= 2 && data[0] == 0x00 && xboxOutputReportID(data[1]) {
		return data[1], data[2:], true
	}
	// Most interrupt OUT payloads include report ID in byte 0.
	if len(data) >= 1 && xboxOutputReportID(data[0]) {
		return data[0], data[1:], true
	}
	return 0, nil, false
}

func (x *XboxElite2) parseXboxOutputReport(setupReportID uint8, data []byte) bool {
	reportID, payload, ok := normalizeXboxOutputReport(setupReportID, data)
	if !ok {
		return false
	}

	var feedback elite2state.OutputState
	switch reportID {
	case ReportIDOutput:
		if len(payload) < OutputReportSize-1 {
			return false
		}
		feedback = elite2state.OutputState{
			RumbleLeft:         payload[0],
			RumbleRight:        payload[1],
			RumbleTriggerLeft:  payload[2],
			RumbleTriggerRight: payload[3],
		}
	case ReportIDOutputRumbleFF:
		if len(payload) < OutputReportSizeRumbleFF-1 {
			return false
		}
		enable := payload[0]
		feedback = elite2state.OutputState{
			// Xbox ff_report carries main motors as strong/weak magnitudes.
			RumbleLeft:         rumblePercentToU8(payload[3]),
			RumbleRight:        rumblePercentToU8(payload[4]),
			RumbleTriggerLeft:  rumblePercentToU8(payload[1]),
			RumbleTriggerRight: rumblePercentToU8(payload[2]),
		}
		if enable&RumbleMaskStrong == 0 {
			feedback.RumbleLeft = 0
		}
		if enable&RumbleMaskWeak == 0 {
			feedback.RumbleRight = 0
		}
		if enable&RumbleMaskTriggerLeft == 0 {
			feedback.RumbleTriggerLeft = 0
		}
		if enable&RumbleMaskTriggerRight == 0 {
			feedback.RumbleTriggerRight = 0
		}
	default:
		return false
	}

	if x.outputFunc != nil {
		x.outputFunc(feedback)
	}
	return true
}

func (x *XboxElite2) HandleControl(bmRequestType, bRequest uint8, wValue, _ uint16, wLength uint16, data []byte) ([]byte, bool) {
	const (
		hidGetReport = 0x01
		hidSetReport = 0x09
	)

	const (
		reportTypeInput   = 0x01
		reportTypeOutput  = 0x02
		reportTypeFeature = 0x03
	)

	reportType := uint8(wValue >> 8)
	reportID := uint8(wValue & 0xFF)

	if bmRequestType == 0xA1 && bRequest == hidGetReport {
		if reportType == reportTypeInput && reportID == ReportIDInput {
			x.stateMu.Lock()
			st := x.inputState
			x.stateMu.Unlock()
			report := x.buildUSBInputReport(st)
			if wLength > 0 && int(wLength) < len(report) {
				return report[:wLength], true
			}
			return report, true
		}

		if reportType == reportTypeFeature && reportID == 0x03 {
			buf := make([]byte, 5)
			buf[0] = 0x03
			// Firmware version placeholder.
			buf[1] = 0x05
			buf[2] = 0x13
			buf[3] = 0x00
			buf[4] = 0x01
			return buf, true
		}

		if reportType == reportTypeFeature {
			size := int(wLength)
			if size <= 0 {
				size = 16
			}
			buf := make([]byte, size)
			buf[0] = reportID
			return buf, true
		}
	}

	if bmRequestType == 0x21 && bRequest == hidSetReport {
		if reportType == reportTypeOutput || reportType == reportTypeFeature {
			// Some hosts tunnel Xbox FF report 0x03 via SetFeature.
			if x.parseXboxOutputReport(reportID, data) {
				return nil, true
			}
			if reportType == reportTypeFeature {
				return nil, true
			}
		}
	}

	slog.Warn("XboxElite2: unsupported control request",
		"bmRequestType", bmRequestType,
		"bRequest", bRequest,
		"reportType", reportType,
		"reportID", reportID)

	return nil, false
}

// clampI16 converts a float64 to int16 with saturation.
func clampI16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32767 {
		return -32767
	}
	return int16(v)
}

// NaksWhenIdle reports that real Elite Series 2 pads are event-driven.
func (x *XboxElite2) NaksWhenIdle() bool { return true }

func (x *XboxElite2) GetDescriptor() *usb.Descriptor {
	return &x.descriptor
}

func (x *XboxElite2) GetDeviceSpecificArgs() map[string]any {
	return map[string]any{
		"profile": x.profile,
	}
}

// buildUSBInputReport builds the host-facing HID report for the currently selected profile.
func (x *XboxElite2) buildUSBInputReport(s elite2state.InputState) []byte {
	return x.buildXboxInputReport(s)
}

func (x *XboxElite2) buildXboxInputReport(s elite2state.InputState) []byte {
	b := make([]byte, InputReportSize)
	b[0] = ReportIDInput

	// Axis order for Xbox BLE profiles: X, Y, Rx, Ry, Z(LT), Rz(RT).
	binary.LittleEndian.PutUint16(b[1:3], stickI16ToU16(s.LX))
	binary.LittleEndian.PutUint16(b[3:5], stickI16ToU16(invertStickY(s.LY)))

	binary.LittleEndian.PutUint16(b[5:7], stickI16ToU16(s.RX))
	binary.LittleEndian.PutUint16(b[7:9], stickI16ToU16(invertStickY(s.RY)))
	// Triggers as 10-bit (0..1023). SDL HIDAPI keys on the descriptor's
	// Logical Max to distinguish triggers (10-bit) from an alt right
	// stick (16-bit) — see SDL_hidapi_xboxone.c:730-770.
	binary.LittleEndian.PutUint16(b[9:11], triggerU8ToU10(s.LT))
	binary.LittleEndian.PutUint16(b[11:13], triggerU8ToU10(s.RT))

	// Hat switch.
	b[13] = dpadBitmaskToHat(s.DPad) & DPadMask

	// Button packing matches real Xbox BLE descriptor (Model 1914):
	// Btn1=A, Btn2=B, Btn3=X, Btn4=Y, Btn5=LB, Btn6=RB, Btn7=View,
	// Btn8=Menu, Btn9=LThumb, Btn10=RThumb, Btn11=Guide, Btn12=unused.
	// Bits 12-15 = padding, Bit 16 = CC Record (Share).
	includePaddles := x.buttonLayout()
	var bits uint32
	if s.Buttons&ButtonA != 0 {
		bits |= 1 << 0
	}
	if s.Buttons&ButtonB != 0 {
		bits |= 1 << 1
	}
	if s.Buttons&ButtonX != 0 {
		bits |= 1 << 2
	}
	if s.Buttons&ButtonY != 0 {
		bits |= 1 << 3
	}
	if s.Buttons&ButtonLB != 0 {
		bits |= 1 << 4
	}
	if s.Buttons&ButtonRB != 0 {
		bits |= 1 << 5
	}
	if s.Buttons&ButtonBack != 0 {
		bits |= 1 << 6
	}
	if s.Buttons&ButtonStart != 0 {
		bits |= 1 << 7
	}
	if s.Buttons&ButtonLThumb != 0 {
		bits |= 1 << 8 // Btn9
	}
	if s.Buttons&ButtonRThumb != 0 {
		bits |= 1 << 9 // Btn10
	}
	if s.Buttons&ButtonGuide != 0 {
		bits |= 1 << 10 // Btn11
	}
	if includePaddles {
		// Paddles use the HHD/InputPlumber order: P2,P1,P4,P3 → Btn12-15.
		if s.Buttons&ButtonP2 != 0 {
			bits |= 1 << 11
		}
		if s.Buttons&ButtonP1 != 0 {
			bits |= 1 << 12
		}
		if s.Buttons&ButtonP4 != 0 {
			bits |= 1 << 13
		}
		if s.Buttons&ButtonP3 != 0 {
			bits |= 1 << 14
		}
	}
	// Bit 15 = padding (always 0).
	// Bit 16 = Consumer Control Record (Share).
	if s.Reserved&ReservedShare != 0 {
		bits |= 1 << 16
	}

	b[14] = byte(bits & 0xFF)
	b[15] = byte((bits >> 8) & 0xFF)
	b[16] = byte((bits >> 16) & 0xFF)

	return b
}
