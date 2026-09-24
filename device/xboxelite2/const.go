package xboxelite2

import "github.com/Alia5/VIIPER/usb"

const (
	DefaultVID uint16 = 0x045E // Microsoft

	// Xbox Wireless Controller Model 1914 (BLE PID 0x0B13). Windows binds
	// its generic HID driver to this PID and respects the report descriptor,
	// so our 17-byte report parses correctly. Other Xbox PIDs (0x0B05 Elite
	// Series 2 BLE, 0x0B00 wired GIP) trigger Microsoft-specific drivers
	// (dc1-controller.inf / xboxgip) that impose hardcoded byte layouts our
	// descriptor doesn't match — those PIDs left the device unusable in
	// testing, so we ship the working PID instead. SDL will identify the
	// device as "Xbox Series X Controller" / "Xbox Wireless Controller".
	DefaultPID uint16 = 0x0B13

	DefaultPIDElite2       uint16 = DefaultPID
	DefaultPIDElite2GIP    uint16 = 0x0B00
	DefaultPIDXboxOneElite uint16 = 0x02E3 // wired Elite 1 (Model 1698)
	DefaultPIDXboxSeries   uint16 = DefaultPID
	DefaultPIDXboxOne      uint16 = 0x02FD // Xbox One S BLE
)

const (
	EndpointIn  = 0x81
	EndpointOut = 0x01
)

const (
	ReportIDInput          = 0x01
	ReportIDOutput         = 0x02
	ReportIDOutputRumbleFF = 0x03
)

const (
	// 1 byte report ID + 16 bytes payload:
	// 6x16-bit axes + hat + buttons + padding.
	InputReportSize = 17
	// 1 byte report ID + 4 bytes rumble payload.
	OutputReportSize = 5
	// 1 byte report ID + 8 bytes force-feedback payload (Xbox BLE-style ff_report).
	OutputReportSizeRumbleFF = 9
)

// Xbox BLE force-feedback motor mask bits (ff_data.enable).
const (
	RumbleMaskWeak         uint8 = 0x01
	RumbleMaskStrong       uint8 = 0x02
	RumbleMaskTriggerRight uint8 = 0x04
	RumbleMaskTriggerLeft  uint8 = 0x08
)

// Button constants for the wire protocol (u16 bitmask, XInput-compatible order).
const (
	ButtonA      uint16 = 0x0001
	ButtonB      uint16 = 0x0002
	ButtonX      uint16 = 0x0004
	ButtonY      uint16 = 0x0008
	ButtonLB     uint16 = 0x0010
	ButtonRB     uint16 = 0x0020
	ButtonBack   uint16 = 0x0040
	ButtonStart  uint16 = 0x0080
	ButtonLThumb uint16 = 0x0100
	ButtonRThumb uint16 = 0x0200
	ButtonGuide  uint16 = 0x0400

	// Elite 2 paddle buttons
	ButtonP1 uint16 = 0x1000
	ButtonP2 uint16 = 0x2000
	ButtonP3 uint16 = 0x4000
	ButtonP4 uint16 = 0x8000
)

// Reserved byte bitmask used by InputState.Reserved.
const (
	ReservedShare uint8 = 0x01 // Share / Capture
)

// DPad wire values (bitmask).
const (
	DPadUp    = 0x01
	DPadDown  = 0x02
	DPadLeft  = 0x04
	DPadRight = 0x08
)

// DPad USB hat values.
// 0 = neutral, 1..8 = Up, UpRight, ..., UpLeft.
const (
	DPadUSBNeutral   = 0x00
	DPadUSBUp        = 0x01
	DPadUSBUpRight   = 0x02
	DPadUSBRight     = 0x03
	DPadUSBDownRight = 0x04
	DPadUSBDown      = 0x05
	DPadUSBDownLeft  = 0x06
	DPadUSBLeft      = 0x07
	DPadUSBUpLeft    = 0x08
)

const DPadMask uint8 = 0x0F

const (
	ProfileElite2       = "elite2"
	ProfileElite2GIP    = "elite2-gip"
	ProfileXboxOne      = "xbox-one"
	ProfileXboxOneElite = "xbox-one-elite"
	ProfileXboxSeries   = "xbox-series"
)

// xboxBLEHIDDescriptor is based on the real Xbox Wireless Controller BLE
// (Model 1914 / PID 0x0B13). Triggers widened from 10-bit to 16-bit
// for Windows HID parser compatibility.
//
// Input report 0x01 layout (17 bytes):
//
//	b[0]    = Report ID (0x01)
//	b[1:3]  = Left Stick X  (uint16 LE, Usage X 0x30, 0-65535, center 32768)
//	b[3:5]  = Left Stick Y  (uint16 LE, Usage Y 0x31, 0-65535, center 32768)
//	b[5:7]  = Right Stick X (uint16 LE, Usage Rx 0x33, 0-65535, center 32768)
//	b[7:9]  = Right Stick Y (uint16 LE, Usage Ry 0x34, 0-65535, center 32768)
//	b[9:11] = Left Trigger   (uint16 LE, Usage Z 0x32, 0-65535)
//	b[11:13]= Right Trigger  (uint16 LE, Usage Rz 0x35, 0-65535)
//	b[13]   = Hat Switch      (4-bit, 0=center/1-8=dirs, +4 pad)
//	b[14:16]= Buttons 1-12   (12 bits: A,B,X,Y,LB,RB,View,Menu,LS,RS,Guide,unused +4 pad)
//	b[16]   = Share/Record    (1 bit Consumer 0x0C:0xB2, +7 pad)
//
// FF output report 0x03: PID page Set Effect Report (8 bytes payload).
var xboxBLEHIDDescriptor = func() []byte {
	return []byte{
		// --- Application Collection (Game Pad) ---
		0x05, 0x01, // Usage Page (Generic Desktop)
		0x09, 0x05, // Usage (Game Pad)
		0xA1, 0x01, // Collection (Application)
		0x85, 0x01, // Report ID (0x01)

		// --- Left stick (X, Y) — one Input each so SDL's parser binds
		//     each usage to its own field unambiguously.
		0x15, 0x00, // Logical Minimum (0)
		0x27, 0xFF, 0xFF, 0x00, 0x00, // Logical Maximum (65535)
		0x75, 0x10, // Report Size (16)
		0x95, 0x01, // Report Count (1)
		0x09, 0x30, // Usage (X)
		0x81, 0x02, // Input (Data,Var,Abs) → bytes 1-2 = LX
		0x09, 0x31, // Usage (Y)
		0x81, 0x02, // Input → bytes 3-4 = LY

		// --- Right stick (Rx, Ry) — separate Input items per axis. ---
		0x09, 0x33, // Usage (Rx)
		0x81, 0x02, // Input → bytes 5-6 = RX
		0x09, 0x34, // Usage (Ry)
		0x81, 0x02, // Input → bytes 7-8 = RY

		// --- Left Trigger (Z) — 10-bit field + 6-bit pad.
		// SDL's HIDAPI Xbox handler keys on bit_size (not Logical Max):
		// 10-bit Z → LEFT_TRIGGER, 16-bit Z → alt RIGHT-STICK X.
		// Our packer writes 0..1023 little-endian into bytes 9-10, so
		// the low 10 bits hold the value and bits 10-15 stay 0 (the pad).
		0x09, 0x32, // Usage (Z)
		0x15, 0x00, // Logical Minimum (0)
		0x26, 0xFF, 0x03, // Logical Maximum (1023)
		0x75, 0x0A, // Report Size (10)
		0x95, 0x01, // Report Count (1)
		0x81, 0x02, // Input (Data,Var,Abs)
		0x15, 0x00, // Logical Minimum (0)
		0x25, 0x00, // Logical Maximum (0)
		0x75, 0x06, // Report Size (6)
		0x95, 0x01, // Report Count (1)
		0x81, 0x03, // Input (Cnst,Var,Abs) — 6-bit pad to byte-align

		// --- Right Trigger (Rz) — 10-bit field + 6-bit pad. ---
		0x09, 0x35, // Usage (Rz)
		0x15, 0x00, // Logical Minimum (0)
		0x26, 0xFF, 0x03, // Logical Maximum (1023)
		0x75, 0x0A, // Report Size (10)
		0x95, 0x01, // Report Count (1)
		0x81, 0x02, // Input (Data,Var,Abs)
		0x15, 0x00, // Logical Minimum (0)
		0x25, 0x00, // Logical Maximum (0)
		0x75, 0x06, // Report Size (6)
		0x95, 0x01, // Report Count (1)
		0x81, 0x03, // Input (Cnst,Var,Abs) — 6-bit pad to byte-align

		// --- Hat Switch (4-bit + 4-bit padding) ---
		0x05, 0x01, // Usage Page (Generic Desktop)
		0x09, 0x39, // Usage (Hat switch)
		0x15, 0x01, // Logical Minimum (1)
		0x25, 0x08, // Logical Maximum (8)
		0x35, 0x00, // Physical Minimum (0)
		0x46, 0x3B, 0x01, // Physical Maximum (315)
		0x66, 0x14, 0x00, // Unit (Eng Rotation: Degrees)
		0x75, 0x04, // Report Size (4)
		0x95, 0x01, // Report Count (1)
		0x81, 0x42, // Input (Data,Var,Abs,Null)
		0x75, 0x04, // Report Size (4)
		0x95, 0x01, // Report Count (1)
		0x15, 0x00, // Logical Minimum (0)
		0x25, 0x00, // Logical Maximum (0)
		0x35, 0x00, // Physical Minimum (0)
		0x45, 0x00, // Physical Maximum (0)
		0x65, 0x00, // Unit (None)
		0x81, 0x03, // Input (Cnst,Var,Abs) — 4-bit padding

		// --- Buttons 1-12 (12 bits + 4-bit padding) ---
		// Btn1-Btn11 = A,B,X,Y,LB,RB,View,Menu,LS,RS,Guide. Btn12 unused.
		// 2026-05-16: reverted from 15-button (P2/P1/P4/P3 at Btn12-15) back
		// to 12-button because xinputhid.sys stopped binding our virtual
		// device on Legion Go 2 when the button count diverged from
		// Microsoft's Xbox Wireless Controller Model 1914 spec. With
		// xinputhid not bound, our HID "Guide" press never raised the
		// XInput Guide subtype event Windows Shell Game Bar listens for,
		// so Game Bar wouldn't open. Restoring the spec-compliant 12-
		// button layout brings xinputhid binding back at the cost of
		// surfacing paddles via the descriptor; bind paddles in games via
		// Steam Input or per-game remap if needed.
		0x05, 0x09, // Usage Page (Button)
		0x19, 0x01, // Usage Minimum (Button 1)
		0x29, 0x0C, // Usage Maximum (Button 12)
		0x15, 0x00, // Logical Minimum (0)
		0x25, 0x01, // Logical Maximum (1)
		0x75, 0x01, // Report Size (1)
		0x95, 0x0C, // Report Count (12)
		0x81, 0x02, // Input (Data,Var,Abs)
		0x15, 0x00, // Logical Minimum (0)
		0x25, 0x00, // Logical Maximum (0)
		0x75, 0x01, // Report Size (1)
		0x95, 0x04, // Report Count (4)
		0x81, 0x03, // Input (Cnst,Var,Abs) — 4-bit padding

		// --- Share/Record (Consumer Control, 1 bit + 7-bit padding) ---
		0x05, 0x0C, // Usage Page (Consumer)
		0x0A, 0xB2, 0x00, // Usage (Record)
		0x15, 0x00, // Logical Minimum (0)
		0x25, 0x01, // Logical Maximum (1)
		0x95, 0x01, // Report Count (1)
		0x75, 0x01, // Report Size (1)
		0x81, 0x02, // Input (Data,Var,Abs)
		0x15, 0x00, // Logical Minimum (0)
		0x25, 0x00, // Logical Maximum (0)
		0x75, 0x07, // Report Size (7)
		0x95, 0x01, // Report Count (1)
		0x81, 0x03, // Input (Cnst,Var,Abs) — 7-bit padding

		// --- Force Feedback Output (Report 0x03, PID page) ---
		0x05, 0x0F, // Usage Page (PID)
		0x09, 0x21, // Usage (Set Effect Report)
		0x85, 0x03, // Report ID (0x03)
		0xA1, 0x02, // Collection (Logical)
		0x09, 0x97, // Usage (DC Enable Actuators)
		0x15, 0x00, // Logical Minimum (0)
		0x25, 0x01, // Logical Maximum (1)
		0x75, 0x04, // Report Size (4)
		0x95, 0x01, // Report Count (1)
		0x91, 0x02, // Output (Data,Var,Abs)
		0x15, 0x00, // Logical Minimum (0)
		0x25, 0x00, // Logical Maximum (0)
		0x75, 0x04, // Report Size (4)
		0x95, 0x01, // Report Count (1)
		0x91, 0x03, // Output (Cnst,Var,Abs) — 4-bit padding
		0x09, 0x70, // Usage (Magnitude)
		0x15, 0x00, // Logical Minimum (0)
		0x25, 0x64, // Logical Maximum (100)
		0x75, 0x08, // Report Size (8)
		0x95, 0x04, // Report Count (4)
		0x91, 0x02, // Output (Data,Var,Abs)
		0x09, 0x50, // Usage (Duration)
		0x66, 0x01, 0x10, // Unit (SI Lin: Time)
		0x55, 0x0E, // Unit Exponent (-2)
		0x15, 0x00, // Logical Minimum (0)
		0x26, 0xFF, 0x00, // Logical Maximum (255)
		0x75, 0x08, // Report Size (8)
		0x95, 0x01, // Report Count (1)
		0x91, 0x02, // Output (Data,Var,Abs)
		0x09, 0xA7, // Usage (Start Delay)
		0x15, 0x00, // Logical Minimum (0)
		0x26, 0xFF, 0x00, // Logical Maximum (255)
		0x75, 0x08, // Report Size (8)
		0x95, 0x01, // Report Count (1)
		0x91, 0x02, // Output (Data,Var,Abs)
		0x65, 0x00, // Unit (None)
		0x55, 0x00, // Unit Exponent (0)
		0x09, 0x7C, // Usage (Loop Count)
		0x15, 0x00, // Logical Minimum (0)
		0x26, 0xFF, 0x00, // Logical Maximum (255)
		0x75, 0x08, // Report Size (8)
		0x95, 0x01, // Report Count (1)
		0x91, 0x02, // Output (Data,Var,Abs)
		0xC0, // End Collection (Logical)
		0xC0, // End Collection (Application)
	}
}()

var defaultDescriptor = usb.Descriptor{
	Device: usb.DeviceDescriptor{
		BcdUSB:          0x0200,
		BDeviceClass:    0x00,
		BDeviceSubClass: 0x00,
		BDeviceProtocol: 0x00,
		BMaxPacketSize0: 0x40,
		IDVendor:        DefaultVID,
		IDProduct:       DefaultPID,
		// Bump revision so Windows refreshes cached HID capabilities after descriptor changes.
		// 0x0513 (2026-05-16): force re-parse + fresh xinputhid binding attempt after
		// reverting from 15-button back to spec-compliant 12-button descriptor — Legion
		// Go 2 hosts that cached the 15-button layout from 0x0512 need a new revision
		// to retry driver matching with the new descriptor shape.
		BcdDevice:     0x0513,
		IManufacturer: 0x01,
		IProduct:      0x02,
		// Provide a stable serial index to help force clean re-enumeration.
		ISerialNumber:      0x03,
		BNumConfigurations: 0x01,
		Speed:              2, // Full speed
	},
	Interfaces: []usb.InterfaceConfig{
		{
			Descriptor: usb.InterfaceDescriptor{
				BInterfaceNumber:   0x00,
				BAlternateSetting:  0x00,
				BNumEndpoints:      0x02,
				BInterfaceClass:    0x03, // HID
				BInterfaceSubClass: 0x00,
				BInterfaceProtocol: 0x00,
				IInterface:         0x00,
			},
			HID: &usb.HIDFunction{
				Descriptor: usb.HIDDescriptor{
					BcdHID:       0x0111,
					BCountryCode: 0x00,
					Descriptors: []usb.HIDSubDescriptor{
						{Type: usb.ReportDescType},
					},
				},
				ReportDescriptorBytes: xboxBLEHIDDescriptor,
			},
			Endpoints: []usb.EndpointDescriptor{
				{
					BEndpointAddress: EndpointIn,
					BMAttributes:     0x03, // Interrupt
					WMaxPacketSize:   32,
					BInterval:        4, // 4ms = 250Hz
				},
				{
					BEndpointAddress: EndpointOut,
					BMAttributes:     0x03, // Interrupt
					WMaxPacketSize:   32,
					BInterval:        4,
				},
			},
		},
	},
	Strings: map[uint8]string{
		0: "Љ", // LangID: en-US (0x0409)
		1: "Microsoft",
		2: "Xbox Wireless Controller",
		3: "VIIPER-XBOX-1914-01",
	},
}
