package switchpro

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/Alia5/VIIPER/device"
	"github.com/Alia5/VIIPER/usb"
	"github.com/Alia5/VIIPER/usbip"
)

// SwitchPro emulates a Nintendo Switch Pro Controller (or Joy-Con) virtual USB device.
type SwitchPro struct {
	gate         *device.InputGate
	inputState   *InputState
	stateMu      sync.Mutex
	outputFunc   func(OutputState)
	descriptor   usb.Descriptor
	profile      string
	timer        uint8 // incrementing report counter
	imuEnabled   bool
	vibEnabled   bool
	playerLights uint8

	// USB handshake state — real Pro Controllers don't send 0x30 reports
	// until the USB handshake (0x80 commands) completes.
	usbReady bool

	// Pending subcommand/USB reply, returned on the next EP IN read.
	pendingReply   []byte
	pendingReplyMu sync.Mutex
}

type switchProCreateOptions struct {
	Profile *string `json:"profile"`
}

func New(o *device.CreateOptions) (*SwitchPro, error) {
	d := &SwitchPro{
		gate:       device.NewInputGate(),
		descriptor: cloneDescriptor(defaultDescriptor),
		profile:    ProfileProController,
		inputState: &InputState{},
	}

	if o != nil && o.DeviceSpecific != "" {
		var args switchProCreateOptions
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
		}
	}

	if o != nil {
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
	case "", ProfileProController, "switchpro", "pro", "procontroller":
		return ProfileProController, nil
	case ProfileJoyConLeft, "joycon-l", "joyconleft", "left":
		return ProfileJoyConLeft, nil
	case ProfileJoyConRight, "joycon-r", "joyconright", "right":
		return ProfileJoyConRight, nil
	default:
		return "", fmt.Errorf(
			"unsupported switchpro profile %q (supported: %q, %q, %q)",
			profile, ProfileProController, ProfileJoyConLeft, ProfileJoyConRight,
		)
	}
}

func (d *SwitchPro) applyProfileDefaults(profile string) {
	d.profile = profile
	switch profile {
	case ProfileJoyConLeft:
		d.descriptor.Device.IDProduct = PIDJoyConLeft
		d.descriptor.Strings[2] = "Joy-Con (L)"
	case ProfileJoyConRight:
		d.descriptor.Device.IDProduct = PIDJoyConRight
		d.descriptor.Strings[2] = "Joy-Con (R)"
	default:
		d.descriptor.Device.IDProduct = PIDProController
		d.descriptor.Strings[2] = "Pro Controller"
	}
}

func (d *SwitchPro) SetOutputCallback(f func(OutputState)) {
	d.outputFunc = f
}

func (d *SwitchPro) UpdateInputState(state *InputState) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	d.inputState = state
	d.gate.Signal()
}

// HandleTransfer processes interrupt IN/OUT transfers for the virtual controller.
func (d *SwitchPro) HandleTransfer(ctx context.Context, ep uint32, dir uint32, out []byte) []byte {
	if dir == usbip.DirIn && ep == 1 {
		// Check for pending subcommand/USB reply first.
		d.pendingReplyMu.Lock()
		reply := d.pendingReply
		d.pendingReply = nil
		d.pendingReplyMu.Unlock()

		if reply != nil {
			return reply
		}

		// No queued reply — block until fresh input (or poll deadline). A
		// pending subcommand reply that arrives while we wait also signals
		// the gate (see handleSubcommand), so it isn't starved.
		switch d.gate.Wait(ctx) {
		case device.GateCancelled:
			return nil
		case device.GateFresh, device.GateDeadline:
			// Re-check for a reply queued while we were blocked.
			d.pendingReplyMu.Lock()
			reply = d.pendingReply
			d.pendingReply = nil
			d.pendingReplyMu.Unlock()
			if reply != nil {
				return reply
			}
		}

		// Build standard 0x30 input report.
		d.stateMu.Lock()
		var st InputState
		if d.inputState != nil {
			st = *d.inputState
		}
		d.stateMu.Unlock()
		return d.buildInputReport(st)
	}

	if dir == usbip.DirOut && ep == 1 {
		if len(out) == 0 {
			return nil
		}
		switch out[0] {
		case ReportIDUSBCmd:
			d.handleUSBCommand(out)
		case ReportIDSubcmd:
			d.handleSubcommand(out)
		case ReportIDRumble:
			d.handleRumbleOnly(out)
		}
	}

	return nil
}

// HandleControl handles EP0 control transfers (HID class requests).
func (d *SwitchPro) HandleControl(bmRequestType, bRequest uint8, wValue, _ uint16, wLength uint16, data []byte) ([]byte, bool) {
	const (
		hidGetReport   = 0x01
		hidSetReport   = 0x09
		hidSetIdle     = 0x0A
		hidSetProtocol = 0x0B
	)
	const (
		reportTypeInput   = 0x01
		reportTypeOutput  = 0x02
		reportTypeFeature = 0x03
	)

	// SET_IDLE and SET_PROTOCOL — accept silently (required for HID enumeration)
	if bmRequestType == 0x21 && (bRequest == hidSetIdle || bRequest == hidSetProtocol) {
		return nil, true
	}

	reportType := uint8(wValue >> 8)
	reportID := uint8(wValue & 0xFF)

	// GET_REPORT
	if bmRequestType == 0xA1 && bRequest == hidGetReport {
		if reportType == reportTypeInput && reportID == ReportIDInput {
			d.stateMu.Lock()
			var st InputState
			if d.inputState != nil {
				st = *d.inputState
			}
			d.stateMu.Unlock()
			report := d.buildInputReport(st)
			if wLength > 0 && int(wLength) < len(report) {
				return report[:wLength], true
			}
			return report, true
		}
		// Return empty for unknown feature/input reports.
		size := int(wLength)
		if size <= 0 {
			size = 64
		}
		buf := make([]byte, size)
		buf[0] = reportID
		return buf, true
	}

	// SET_REPORT
	if bmRequestType == 0x21 && bRequest == hidSetReport {
		if reportType == reportTypeOutput {
			// Handle as if it came on the interrupt endpoint.
			if len(data) > 0 {
				switch data[0] {
				case ReportIDSubcmd:
					d.handleSubcommand(data)
				case ReportIDRumble:
					d.handleRumbleOnly(data)
				case ReportIDUSBCmd:
					d.handleUSBCommand(data)
				}
			}
			return nil, true
		}
		return nil, true
	}

	slog.Warn("SwitchPro: unsupported control request",
		"bmRequestType", bmRequestType,
		"bRequest", bRequest,
		"reportType", reportType,
		"reportID", reportID)

	return nil, false
}

func (d *SwitchPro) GetDescriptor() *usb.Descriptor {
	return &d.descriptor
}

func (d *SwitchPro) GetDeviceSpecificArgs() map[string]any {
	return map[string]any{
		"profile": d.profile,
	}
}

// buildInputReport builds a standard 0x30 full input report (64 bytes).
func (d *SwitchPro) buildInputReport(s InputState) []byte {
	b := make([]byte, InputReportSize)
	b[0] = ReportIDInput
	d.timer++
	b[1] = d.timer
	b[2] = BatteryFull // Battery full, USB connected

	// Buttons: 3 bytes (bytes 3-5)
	b[3] = byte(s.Buttons & 0xFF)         // Right-side buttons
	b[4] = byte((s.Buttons >> 8) & 0xFF)  // Shared buttons
	b[5] = byte((s.Buttons >> 16) & 0xFF) // Left-side buttons

	// Left stick: 12-bit packed (bytes 6-8)
	// Switch Y axis: positive = UP (higher value)
	// Our wire format: int16, positive = UP (XInput convention)
	// So we DON'T invert Y.
	ls := packStick12(s.LX, s.LY)
	b[6] = ls[0]
	b[7] = ls[1]
	b[8] = ls[2]

	// Right stick: 12-bit packed (bytes 9-11)
	rs := packStick12(s.RX, s.RY)
	b[9] = rs[0]
	b[10] = rs[1]
	b[11] = rs[2]

	// Byte 12: vibrator input report (0x00)
	b[12] = 0x00

	// IMU data: 3 frames × 12 bytes = 36 bytes (bytes 13-48)
	// Duplicate the current frame for all 3 samples.
	if d.imuEnabled {
		for frame := 0; frame < 3; frame++ {
			base := 13 + frame*12
			binary.LittleEndian.PutUint16(b[base:base+2], uint16(s.AccelX))
			binary.LittleEndian.PutUint16(b[base+2:base+4], uint16(s.AccelY))
			binary.LittleEndian.PutUint16(b[base+4:base+6], uint16(s.AccelZ))
			binary.LittleEndian.PutUint16(b[base+6:base+8], uint16(s.GyroX))
			binary.LittleEndian.PutUint16(b[base+8:base+10], uint16(s.GyroY))
			binary.LittleEndian.PutUint16(b[base+10:base+12], uint16(s.GyroZ))
		}
	}

	return b
}

// handleRumbleOnly processes report 0x10 (rumble data only, no subcommand).
func (d *SwitchPro) handleRumbleOnly(data []byte) {
	// data[0] = 0x10 (report ID)
	// data[1] = global packet number
	// data[2-9] = rumble data (8 bytes)
	if len(data) < 10 {
		return
	}
	left, right := parseRumble(data[2:10])
	if d.outputFunc != nil && d.vibEnabled {
		d.outputFunc(OutputState{RumbleLeft: left, RumbleRight: right})
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
