// Package xboxgip provides an Xbox Series X|S controller device implementation
// using the GIP (Game Input Protocol) as specified in MS-GIPUSB.
package xboxgip

// GIP message types (command IDs).
const (
	GIPAcknowledge  = 0x01 // Protocol control / ACK
	GIPAnnounce     = 0x02 // Device arrival (Hello)
	GIPStatus       = 0x03 // Device status (battery, etc.)
	GIPDescriptor   = 0x04 // Metadata request/response
	GIPSetState     = 0x05 // Set device state (power control)
	GIPAuthenticate = 0x06 // Authentication handshake
	GIPVirtualKey   = 0x07 // Guide button / virtual keycode
	GIPAudioControl = 0x08 // Audio format/volume
	GIPRumble       = 0x09 // Force feedback
	GIPLedControl   = 0x0A // LED mode
	GIPHIDReport    = 0x0B // Standard HID report
	GIPFirmware     = 0x0C // Firmware data
	GIPSerialNumber = 0x1E // Serial number
	GIPInput        = 0x20 // Gamepad input (low-latency)
	GIPAudioSamples = 0x60 // PCM audio
)

// GIP flag bits (byte 1 of GIP header).
const (
	GIPFlagFragment = 0x80 // Fragmented message
	GIPFlagInitFrag = 0x40 // First fragment (has total length)
	GIPFlagSystem   = 0x20 // System message
	GIPFlagACME     = 0x10 // Requires acknowledgement
)

// GIP device power states (payload of SetState command 0x05).
const (
	GIPStateStart   = 0x00 // Transition to Active
	GIPStateStop    = 0x01 // Transition to Idle
	GIPStateFullPwr = 0x03 // Reset idle timer (wireless)
	GIPStateOff     = 0x04 // Power off
	GIPStateQuiesce = 0x05 // Clear motors (Guide pressed)
	GIPStateReset   = 0x07 // Full device reset
)

// GIP protocol state machine.
const (
	stateArrival = iota
	stateIdle
	stateActive
)

// GIP gamepad button bitmasks (uint16 LE in input report).
// These differ from XInput button order.
const (
	// Low byte (offset 4 of GIP input report)
	GIPBtnReserved = 0x0001
	GIPBtnSync     = 0x0002 // Keep-alive / Sync
	GIPBtnMenu     = 0x0004 // Start/Menu
	GIPBtnView     = 0x0008 // Back/View/Select
	GIPBtnA        = 0x0010
	GIPBtnB        = 0x0020
	GIPBtnX        = 0x0040
	GIPBtnY        = 0x0080

	// High byte (offset 5 of GIP input report)
	GIPBtnDPadUp    = 0x0100
	GIPBtnDPadDown  = 0x0200
	GIPBtnDPadLeft  = 0x0400
	GIPBtnDPadRight = 0x0800
	GIPBtnLB        = 0x1000
	GIPBtnRB        = 0x2000
	GIPBtnLStick    = 0x4000
	GIPBtnRStick    = 0x8000
)

// XInput button bitmasks (for mapping from C# InputState).
const (
	xinputDPadUp    = 0x0001
	xinputDPadDown  = 0x0002
	xinputDPadLeft  = 0x0004
	xinputDPadRight = 0x0008
	xinputStart     = 0x0010
	xinputBack      = 0x0020
	xinputLThumb    = 0x0040
	xinputRThumb    = 0x0080
	xinputLB        = 0x0100
	xinputRB        = 0x0200
	xinputGuide     = 0x0400
	xinputA         = 0x1000
	xinputB         = 0x2000
	xinputX         = 0x4000
	xinputY         = 0x8000
)

// InputStateSize is the wire size (C# → Go) matching xbox360 format: 20 bytes.
const InputStateSize = 20

// OutputStateSize is the wire size (Go → C#): 4 bytes (4 motors).
const OutputStateSize = 4

// GIP input report payload size: 14 bytes.
const gipInputPayloadSize = 14

// GIP input report total size: 4-byte header + 14-byte payload = 18 bytes.
const gipInputReportSize = 18

// helloIntervalMs is the spec-defined Hello retransmit cadence (500 ms) while
// the device is in Arrival state waiting for the host to issue a metadata
// request or SetState. Per MS-GIPUSB §2.1.1.2.
const helloIntervalMs = 500

// assumeActiveDelayMs is the post-attach window we wait for the host to issue
// a metadata-request (0x04) or SetState (0x05) before assuming the host has
// already processed our identity from cached metadata and is just waiting for
// us to drive ourselves into the Active state. Empirically, real Xbox One
// controllers receive 0x04/0x05 ~30-60s post-enumeration, but USBIP-presented
// devices appear to never receive them — yet cached PIDs still get downstream
// rumble (0x09), suggesting the publication path is gated on us reaching
// Active rather than on the host completing metadata exchange.
const assumeActiveDelayMs = 1500
