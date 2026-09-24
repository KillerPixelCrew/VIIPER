// Package elite2 defines the wire format shared by VIIPER device backends
// whose host-facing reports diverge but whose client-to-server input stream
// is identical. The format derives from the Xbox Elite 2 BLE shape (buttons,
// sticks, triggers, dpad, IMU) and was later extended with Steam touchpad
// fields. Both the xboxelite2 and steamcontroller device packages consume
// this same byte layout — Xbox profiles ignore the touchpad/IMU tail, Steam
// profiles pack it into the Steam Deck vendor report.
//
// This package is deliberately neutral: it does not depend on any device
// package, so neither xboxelite2 nor steamcontroller appears to "own" the
// wire format.
package elite2

import (
	"encoding/binary"
	"io"
)

// nolint
// viiper:wire xboxelite2 c2s buttons:u16 lt:u8 rt:u8 lx:i16 ly:i16 rx:i16 ry:i16 dpad:u8 _:u8 gyroX:i16 gyroY:i16 gyroZ:i16 accelX:i16 accelY:i16 accelZ:i16 touchFlags:u8 rPadX:i16 rPadY:i16 rPadForce:u16
type InputState struct {
	Buttons uint16
	LT, RT  uint8
	LX, LY  int16
	RX, RY  int16
	DPad    uint8
	// Reserved bit 0 is used as share/capture.
	Reserved uint8
	// Steam Deck profiles consume these directly into bytes 24..35.
	GyroX, GyroY, GyroZ    int16
	AccelX, AccelY, AccelZ int16
	// Steam touchpad extensions (right pad only).
	TouchFlags uint8
	RPadX      int16
	RPadY      int16
	RPadForce  uint16
}

const (
	// 2+1+1+2+2+2+2+1+1 + 6*2 + 1 + 2 + 2 + 2
	InputStateSize = 33
	// Previous wire format used before Steam touchpad fields were added.
	// Layout is the first 26 bytes of InputState.
	InputStateV1Size = 26
	// Legacy wire format used before IMU forwarding was added.
	// Layout is the first 14 bytes of InputState.
	LegacyInputStateSize = 14
)

const (
	TouchFlagRightPadTouch uint8 = 0x01
	TouchFlagRightPadPress uint8 = 0x02
)

func (s *InputState) MarshalBinary() ([]byte, error) {
	b := make([]byte, InputStateSize)
	binary.LittleEndian.PutUint16(b[0:2], s.Buttons)
	b[2] = s.LT
	b[3] = s.RT
	binary.LittleEndian.PutUint16(b[4:6], uint16(s.LX))
	binary.LittleEndian.PutUint16(b[6:8], uint16(s.LY))
	binary.LittleEndian.PutUint16(b[8:10], uint16(s.RX))
	binary.LittleEndian.PutUint16(b[10:12], uint16(s.RY))
	b[12] = s.DPad
	b[13] = s.Reserved
	binary.LittleEndian.PutUint16(b[14:16], uint16(s.GyroX))
	binary.LittleEndian.PutUint16(b[16:18], uint16(s.GyroY))
	binary.LittleEndian.PutUint16(b[18:20], uint16(s.GyroZ))
	binary.LittleEndian.PutUint16(b[20:22], uint16(s.AccelX))
	binary.LittleEndian.PutUint16(b[22:24], uint16(s.AccelY))
	binary.LittleEndian.PutUint16(b[24:26], uint16(s.AccelZ))
	b[26] = s.TouchFlags
	binary.LittleEndian.PutUint16(b[27:29], uint16(s.RPadX))
	binary.LittleEndian.PutUint16(b[29:31], uint16(s.RPadY))
	binary.LittleEndian.PutUint16(b[31:33], s.RPadForce)
	return b, nil
}

func (s *InputState) UnmarshalBinary(data []byte) error {
	if len(data) < InputStateSize {
		return io.ErrUnexpectedEOF
	}
	s.Buttons = binary.LittleEndian.Uint16(data[0:2])
	s.LT = data[2]
	s.RT = data[3]
	s.LX = int16(binary.LittleEndian.Uint16(data[4:6]))
	s.LY = int16(binary.LittleEndian.Uint16(data[6:8]))
	s.RX = int16(binary.LittleEndian.Uint16(data[8:10]))
	s.RY = int16(binary.LittleEndian.Uint16(data[10:12]))
	s.DPad = data[12]
	s.Reserved = data[13]
	s.GyroX = int16(binary.LittleEndian.Uint16(data[14:16]))
	s.GyroY = int16(binary.LittleEndian.Uint16(data[16:18]))
	s.GyroZ = int16(binary.LittleEndian.Uint16(data[18:20]))
	s.AccelX = int16(binary.LittleEndian.Uint16(data[20:22]))
	s.AccelY = int16(binary.LittleEndian.Uint16(data[22:24]))
	s.AccelZ = int16(binary.LittleEndian.Uint16(data[24:26]))
	s.TouchFlags = data[26]
	s.RPadX = int16(binary.LittleEndian.Uint16(data[27:29]))
	s.RPadY = int16(binary.LittleEndian.Uint16(data[29:31]))
	s.RPadForce = binary.LittleEndian.Uint16(data[31:33])
	return nil
}

func (s *InputState) UnmarshalV1Binary(data []byte) error {
	if len(data) < InputStateV1Size {
		return io.ErrUnexpectedEOF
	}
	s.Buttons = binary.LittleEndian.Uint16(data[0:2])
	s.LT = data[2]
	s.RT = data[3]
	s.LX = int16(binary.LittleEndian.Uint16(data[4:6]))
	s.LY = int16(binary.LittleEndian.Uint16(data[6:8]))
	s.RX = int16(binary.LittleEndian.Uint16(data[8:10]))
	s.RY = int16(binary.LittleEndian.Uint16(data[10:12]))
	s.DPad = data[12]
	s.Reserved = data[13]
	s.GyroX = int16(binary.LittleEndian.Uint16(data[14:16]))
	s.GyroY = int16(binary.LittleEndian.Uint16(data[16:18]))
	s.GyroZ = int16(binary.LittleEndian.Uint16(data[18:20]))
	s.AccelX = int16(binary.LittleEndian.Uint16(data[20:22]))
	s.AccelY = int16(binary.LittleEndian.Uint16(data[22:24]))
	s.AccelZ = int16(binary.LittleEndian.Uint16(data[24:26]))
	s.TouchFlags = 0
	s.RPadX = 0
	s.RPadY = 0
	s.RPadForce = 0
	return nil
}

func (s *InputState) UnmarshalLegacyBinary(data []byte) error {
	if len(data) < LegacyInputStateSize {
		return io.ErrUnexpectedEOF
	}
	s.Buttons = binary.LittleEndian.Uint16(data[0:2])
	s.LT = data[2]
	s.RT = data[3]
	s.LX = int16(binary.LittleEndian.Uint16(data[4:6]))
	s.LY = int16(binary.LittleEndian.Uint16(data[6:8]))
	s.RX = int16(binary.LittleEndian.Uint16(data[8:10]))
	s.RY = int16(binary.LittleEndian.Uint16(data[10:12]))
	s.DPad = data[12]
	s.Reserved = data[13]
	// Legacy format had no IMU fields.
	s.GyroX = 0
	s.GyroY = 0
	s.GyroZ = 0
	s.AccelX = 0
	s.AccelY = 0
	s.AccelZ = 0
	s.TouchFlags = 0
	s.RPadX = 0
	s.RPadY = 0
	s.RPadForce = 0
	return nil
}

// nolint
// viiper:wire xboxelite2 s2c rumbleLeft:u8 rumbleRight:u8 rumbleTriggerLeft:u8 rumbleTriggerRight:u8
type OutputState struct {
	RumbleLeft         uint8
	RumbleRight        uint8
	RumbleTriggerLeft  uint8
	RumbleTriggerRight uint8
}

const OutputStateSize = 4

func (o *OutputState) MarshalBinary() ([]byte, error) {
	return []byte{
		o.RumbleLeft,
		o.RumbleRight,
		o.RumbleTriggerLeft,
		o.RumbleTriggerRight,
	}, nil
}

func (o *OutputState) UnmarshalBinary(data []byte) error {
	if len(data) < OutputStateSize {
		return io.ErrUnexpectedEOF
	}
	o.RumbleLeft = data[0]
	o.RumbleRight = data[1]
	o.RumbleTriggerLeft = data[2]
	o.RumbleTriggerRight = data[3]
	return nil
}
