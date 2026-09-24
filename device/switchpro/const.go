package switchpro

import "github.com/Alia5/VIIPER/usb"

const (
	DefaultVID       uint16 = 0x057E // Nintendo Co., Ltd.
	PIDProController uint16 = 0x2009
	PIDJoyConLeft    uint16 = 0x2006
	PIDJoyConRight   uint16 = 0x2007
)

const (
	EndpointIn  = 0x81
	EndpointOut = 0x01
)

// Report IDs used by the Pro Controller protocol.
const (
	ReportIDInput       = 0x30 // Standard full input report
	ReportIDSubcmdReply = 0x21 // Subcommand reply
	ReportIDRumble      = 0x10 // Rumble-only output
	ReportIDSubcmd      = 0x01 // Subcommand output (rumble + subcmd)
	ReportIDUSBCmd      = 0x80 // USB vendor command
	ReportIDUSBReply    = 0x81 // USB vendor reply
)

const (
	InputReportSize  = 64
	OutputReportSize = 64
)

// Button constants for the wire protocol (uint32 bitmask).
// Packed as: byte0 | (byte1 << 8) | (byte2 << 16), matching the 3-byte
// Pro Controller button layout in the 0x30 report (bytes 3-5).
//
// Byte 0 (right-side buttons):
const (
	ButtonY  uint32 = 0x000001
	ButtonX  uint32 = 0x000002
	ButtonB  uint32 = 0x000004
	ButtonA  uint32 = 0x000008
	ButtonSR uint32 = 0x000010 // Joy-Con only
	ButtonSL uint32 = 0x000020 // Joy-Con only
	ButtonR  uint32 = 0x000040
	ButtonZR uint32 = 0x000080
)

// Byte 1 (shared buttons):
const (
	ButtonMinus   uint32 = 0x000100
	ButtonPlus    uint32 = 0x000200
	ButtonRStick  uint32 = 0x000400
	ButtonLStick  uint32 = 0x000800
	ButtonHome    uint32 = 0x001000
	ButtonCapture uint32 = 0x002000
)

// Byte 2 (left-side buttons):
const (
	ButtonDDown  uint32 = 0x010000
	ButtonDUp    uint32 = 0x020000
	ButtonDRight uint32 = 0x040000
	ButtonDLeft  uint32 = 0x080000
	ButtonSRLeft uint32 = 0x100000 // Joy-Con (L) only
	ButtonSLLeft uint32 = 0x200000 // Joy-Con (L) only
	ButtonL      uint32 = 0x400000
	ButtonZL     uint32 = 0x800000
)

// Profile names for the device variant.
const (
	ProfileProController = "pro-controller"
	ProfileJoyConLeft    = "joycon-left"
	ProfileJoyConRight   = "joycon-right"
)

// Subcommand IDs.
const (
	SubcmdDeviceInfo      = 0x02
	SubcmdSetInputMode    = 0x03
	SubcmdTriggerElapsed  = 0x04
	SubcmdSetShipment     = 0x08
	SubcmdSPIFlashRead    = 0x10
	SubcmdSetPlayerLights = 0x30
	SubcmdSetHomeLED      = 0x38
	SubcmdEnableIMU       = 0x40
	SubcmdEnableVibration = 0x48
)

// USB command IDs (report 0x80).
const (
	USBCmdRequestMAC  = 0x01
	USBCmdHandshake   = 0x02
	USBCmdSetBaudrate = 0x03
	USBCmdForceHID    = 0x04
)

// Battery / connection status byte (byte 2 of 0x30 report).
const BatteryFull uint8 = 0x8E // Battery full, USB connected

// Pro Controller HID report descriptor — exact bytes from real hardware.
// Source: ToadKing/dekuNukem Pro Controller USB captures.
var proControllerHIDDescriptor = []byte{
	0x05, 0x01, // Usage Page (Generic Desktop Ctrls)
	0x15, 0x00, // Logical Minimum (0)
	0x09, 0x04, // Usage (Joystick)
	0xA1, 0x01, // Collection (Application)
	0x85, 0x30, //   Report ID (0x30) — standard full input
	0x05, 0x01, //   Usage Page (Generic Desktop Ctrls)
	0x05, 0x09, //   Usage Page (Button)
	0x19, 0x01, //   Usage Minimum (1)
	0x29, 0x0A, //   Usage Maximum (10)
	0x15, 0x00, //   Logical Minimum (0)
	0x25, 0x01, //   Logical Maximum (1)
	0x75, 0x01, //   Report Size (1)
	0x95, 0x0A, //   Report Count (10)
	0x55, 0x00, //   Unit Exponent (0)
	0x65, 0x00, //   Unit (None)
	0x81, 0x02, //   Input (Data,Var,Abs)
	0x05, 0x09, //   Usage Page (Button)
	0x19, 0x0B, //   Usage Minimum (11)
	0x29, 0x0E, //   Usage Maximum (14)
	0x15, 0x00, //   Logical Minimum (0)
	0x25, 0x01, //   Logical Maximum (1)
	0x75, 0x01, //   Report Size (1)
	0x95, 0x04, //   Report Count (4)
	0x81, 0x02, //   Input (Data,Var,Abs)
	0x75, 0x01, //   Report Size (1)
	0x95, 0x02, //   Report Count (2)
	0x81, 0x03, //   Input (Const) — padding
	0x0B, 0x01, 0x00, 0x01, 0x00, //   Usage (Generic Desktop:Pointer)
	0xA1, 0x00, //   Collection (Physical)
	0x0B, 0x30, 0x00, 0x01, 0x00, //     Usage (X)
	0x0B, 0x31, 0x00, 0x01, 0x00, //     Usage (Y)
	0x0B, 0x32, 0x00, 0x01, 0x00, //     Usage (Z)
	0x0B, 0x35, 0x00, 0x01, 0x00, //     Usage (Rz)
	0x15, 0x00, //     Logical Minimum (0)
	0x27, 0xFF, 0xFF, 0x00, 0x00, //     Logical Maximum (65535)
	0x75, 0x10, //     Report Size (16)
	0x95, 0x04, //     Report Count (4)
	0x81, 0x02, //     Input (Data,Var,Abs)
	0xC0,                         //   End Collection (Physical)
	0x0B, 0x39, 0x00, 0x01, 0x00, //   Usage (Hat switch)
	0x15, 0x00, //   Logical Minimum (0)
	0x25, 0x07, //   Logical Maximum (7)
	0x35, 0x00, //   Physical Minimum (0)
	0x46, 0x3B, 0x01, //   Physical Maximum (315)
	0x65, 0x14, //   Unit (Degrees)
	0x75, 0x04, //   Report Size (4)
	0x95, 0x01, //   Report Count (1)
	0x81, 0x02, //   Input (Data,Var,Abs)
	0x05, 0x09, //   Usage Page (Button)
	0x19, 0x0F, //   Usage Minimum (15)
	0x29, 0x12, //   Usage Maximum (18)
	0x15, 0x00, //   Logical Minimum (0)
	0x25, 0x01, //   Logical Maximum (1)
	0x75, 0x01, //   Report Size (1)
	0x95, 0x04, //   Report Count (4)
	0x81, 0x02, //   Input (Data,Var,Abs)
	0x75, 0x08, //   Report Size (8)
	0x95, 0x34, //   Report Count (52)
	0x81, 0x03, //   Input (Const) — padding
	0x06, 0x00, 0xFF, //   Usage Page (Vendor Defined 0xFF00)
	0x85, 0x21, //   Report ID (0x21) — subcommand reply
	0x09, 0x01, //   Usage (Vendor Usage 1)
	0x75, 0x08, //   Report Size (8)
	0x95, 0x3F, //   Report Count (63)
	0x81, 0x03, //   Input (Const)
	0x85, 0x81, //   Report ID (0x81) — USB vendor reply
	0x09, 0x02, //   Usage (Vendor Usage 2)
	0x75, 0x08, //   Report Size (8)
	0x95, 0x3F, //   Report Count (63)
	0x81, 0x03, //   Input (Const)
	0x85, 0x01, //   Report ID (0x01) — subcommand output
	0x09, 0x03, //   Usage (Vendor Usage 3)
	0x75, 0x08, //   Report Size (8)
	0x95, 0x3F, //   Report Count (63)
	0x91, 0x83, //   Output (Const,Var,Abs,Volatile)
	0x85, 0x10, //   Report ID (0x10) — rumble only output
	0x09, 0x04, //   Usage (Vendor Usage 4)
	0x75, 0x08, //   Report Size (8)
	0x95, 0x3F, //   Report Count (63)
	0x91, 0x83, //   Output (Const,Var,Abs,Volatile)
	0x85, 0x80, //   Report ID (0x80) — USB vendor command
	0x09, 0x05, //   Usage (Vendor Usage 5)
	0x75, 0x08, //   Report Size (8)
	0x95, 0x3F, //   Report Count (63)
	0x91, 0x83, //   Output (Const,Var,Abs,Volatile)
	0x85, 0x82, //   Report ID (0x82) — vendor output
	0x09, 0x06, //   Usage (Vendor Usage 6)
	0x75, 0x08, //   Report Size (8)
	0x95, 0x3F, //   Report Count (63)
	0x91, 0x83, //   Output (Const,Var,Abs,Volatile)
	0xC0, // End Collection
}

var defaultDescriptor = usb.Descriptor{
	Device: usb.DeviceDescriptor{
		BcdUSB:             0x0200,
		BDeviceClass:       0x00,
		BDeviceSubClass:    0x00,
		BDeviceProtocol:    0x00,
		BMaxPacketSize0:    0x40,
		IDVendor:           DefaultVID,
		IDProduct:          PIDProController,
		BcdDevice:          0x0210,
		IManufacturer:      0x01,
		IProduct:           0x02,
		ISerialNumber:      0x00,
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
				ReportDescriptorBytes: proControllerHIDDescriptor,
			},
			Endpoints: []usb.EndpointDescriptor{
				{
					BEndpointAddress: EndpointIn,
					BMAttributes:     0x03, // Interrupt
					WMaxPacketSize:   64,
					BInterval:        8, // 8ms = 125Hz
				},
				{
					BEndpointAddress: EndpointOut,
					BMAttributes:     0x03, // Interrupt
					WMaxPacketSize:   64,
					BInterval:        8,
				},
			},
		},
	},
	Strings: map[uint8]string{
		0: "\x04\x09",
		1: "Nintendo Co., Ltd.",
		2: "Pro Controller",
	},
}
