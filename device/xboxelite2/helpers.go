package xboxelite2

// stickI16ToU16 converts signed i16 (-32768..32767) to unsigned u16 (0..65535).
func stickI16ToU16(v int16) uint16 {
	return uint16(int32(v) + 32768)
}

// invertStickY flips vertical stick direction while preserving full int16 range.
func invertStickY(v int16) int16 {
	if v == -32768 {
		return 32767
	}
	return -v
}

// triggerU8ToU10 scales trigger range 0..255 to 0..1023.
func triggerU8ToU10(v uint8) uint16 {
	return uint16((uint32(v)*1023 + 127) / 255)
}

// rumblePercentToU8 converts 0..100 force-feedback magnitudes to 0..255.
// Values above 100 are passed through to handle non-standard stacks.
func rumblePercentToU8(v uint8) uint8 {
	if v > 100 {
		return v
	}
	return uint8((uint16(v)*255 + 50) / 100)
}

// dpadBitmaskToHat converts DPad bitmask into HID hat values:
// 0 = neutral, 1..8 = Up, UpRight, Right, DownRight, Down, DownLeft, Left, UpLeft.
func dpadBitmaskToHat(dpad uint8) uint8 {
	switch {
	case dpad&DPadUp != 0 && dpad&DPadRight != 0:
		return DPadUSBUpRight
	case dpad&DPadUp != 0 && dpad&DPadLeft != 0:
		return DPadUSBUpLeft
	case dpad&DPadDown != 0 && dpad&DPadRight != 0:
		return DPadUSBDownRight
	case dpad&DPadDown != 0 && dpad&DPadLeft != 0:
		return DPadUSBDownLeft
	case dpad&DPadUp != 0:
		return DPadUSBUp
	case dpad&DPadDown != 0:
		return DPadUSBDown
	case dpad&DPadLeft != 0:
		return DPadUSBLeft
	case dpad&DPadRight != 0:
		return DPadUSBRight
	default:
		return DPadUSBNeutral
	}
}
