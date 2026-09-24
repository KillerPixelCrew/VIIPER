package xboxgip

import (
	"encoding/binary"
	"io"
)

// InputState is the wire format received from C# (same 20-byte layout as xbox360).
// Go remaps buttons and scales triggers when building the GIP report.
//
// Reserved-byte allocation (helper → libviiper extensions):
//
//	Reserved[0] = Elite 2 paddle byte (P1=0x01, P2=0x02, P3=0x04, P4=0x08)
//	              sent inline in 46-byte input report when PID is 0x0B00.
//	Reserved[1] = Elite 2 paddle "mode" byte (0 = paddles unmapped /
//	              forwarded as paddle events; non-zero = mapped, SDL
//	              suppresses to avoid double-fire with re-mapped buttons).
//	Reserved[2..5] = unused (zero).
//
// viiper:wire xboxgip c2s buttons:u32 lt:u8 rt:u8 lx:i16 ly:i16 rx:i16 ry:i16 paddles:u8 paddleMode:u8 _:u8 _:u8 _:u8 _:u8
type InputState struct {
	Buttons  uint32
	LT, RT   uint8
	LX, LY   int16
	RX, RY   int16
	Reserved [6]byte
}

// Paddles returns the Elite 2 paddle bitmap stored in Reserved[0].
func (s *InputState) Paddles() byte { return s.Reserved[0] }

// PaddleMode returns the Elite 2 paddle mode flag stored in Reserved[1].
func (s *InputState) PaddleMode() byte { return s.Reserved[1] }

// UnmarshalBinary decodes 20 bytes into InputState.
func (s *InputState) UnmarshalBinary(data []byte) error {
	if len(data) < InputStateSize {
		return io.ErrUnexpectedEOF
	}
	s.Buttons = binary.LittleEndian.Uint32(data[0:4])
	s.LT = data[4]
	s.RT = data[5]
	s.LX = int16(binary.LittleEndian.Uint16(data[6:8]))
	s.LY = int16(binary.LittleEndian.Uint16(data[8:10]))
	s.RX = int16(binary.LittleEndian.Uint16(data[10:12]))
	s.RY = int16(binary.LittleEndian.Uint16(data[12:14]))
	copy(s.Reserved[:], data[14:20])
	return nil
}

// MarshalBinary encodes InputState to 20 bytes.
func (s *InputState) MarshalBinary() ([]byte, error) {
	b := make([]byte, InputStateSize)
	binary.LittleEndian.PutUint32(b[0:4], s.Buttons)
	b[4] = s.LT
	b[5] = s.RT
	binary.LittleEndian.PutUint16(b[6:8], uint16(s.LX))
	binary.LittleEndian.PutUint16(b[8:10], uint16(s.LY))
	binary.LittleEndian.PutUint16(b[10:12], uint16(s.RX))
	binary.LittleEndian.PutUint16(b[12:14], uint16(s.RY))
	copy(b[14:20], s.Reserved[:])
	return b, nil
}

// OutputState is the wire format sent to C# for force feedback (4 bytes).
//
// viiper:wire xboxgip s2c leftMotor:u8 rightMotor:u8 leftTrigger:u8 rightTrigger:u8
type OutputState struct {
	LeftMotor    uint8
	RightMotor   uint8
	LeftTrigger  uint8
	RightTrigger uint8
}

// MarshalBinary encodes OutputState to 4 bytes.
func (o *OutputState) MarshalBinary() ([]byte, error) {
	return []byte{o.LeftMotor, o.RightMotor, o.LeftTrigger, o.RightTrigger}, nil
}

// UnmarshalBinary decodes 4 bytes into OutputState.
func (o *OutputState) UnmarshalBinary(data []byte) error {
	if len(data) < OutputStateSize {
		return io.ErrUnexpectedEOF
	}
	o.LeftMotor = data[0]
	o.RightMotor = data[1]
	o.LeftTrigger = data[2]
	o.RightTrigger = data[3]
	return nil
}
