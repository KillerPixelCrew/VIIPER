package steamdeck

import (
	"encoding/binary"
	"io"
)

// viiper:wire steamdeck c2s a:bool x:bool b:bool y:bool l1:bool r1:bool l2Digital:bool r2Digital:bool l5:bool menu:bool steam:bool options:bool dpadDown:bool dpadLeft:bool dpadRight:bool dpadUp:bool l3:bool rPadTouch:bool lPadTouch:bool rPadPress:bool lPadPress:bool r5:bool r3:bool rStickTouch:bool lStickTouch:bool r4:bool l4:bool quickAccess:bool lPadX:i16 lPadY:i16 rPadX:i16 rPadY:i16 accelX:i16 accelY:i16 accelZ:i16 pitch:i16 yaw:i16 roll:i16 gyroQuatW:i16 gyroQuatX:i16 gyroQuatY:i16 gyroQuatZ:i16 lTrigger:u16 rTrigger:u16 lStickX:i16 lStickY:i16 rStickX:i16 rStickY:i16 lPadForce:u16 rPadForce:u16 lStickForce:u16 rStickForce:u16
type InputState struct {
	A, X, B, Y     bool
	L1, R1         bool
	L2Digital      bool
	R2Digital      bool
	L5, Menu       bool
	Steam, Options bool
	DPadDown       bool
	DPadLeft       bool
	DPadRight      bool
	DPadUp         bool
	L3             bool
	RPadTouch      bool
	LPadTouch      bool
	RPadPress      bool
	LPadPress      bool
	R5             bool
	R3             bool
	RStickTouch    bool
	LStickTouch    bool
	R4             bool
	L4             bool
	QuickAccess    bool
	Reserved15     uint8
	LPadX, LPadY   int16
	RPadX, RPadY   int16
	// Steam/SDL decode the raw deck IMU slots as application-space axes X, Z, -Y.
	// Callers starting from that application-space basis must therefore encode rawY=-Z and rawZ=Y.
	AccelX, AccelY int16
	AccelZ         int16
	// These historical field names occupy the raw gyro X/Y/Z slots in the packet.
	Pitch, Yaw, Roll int16
	GyroQuatW        int16
	GyroQuatX        int16
	GyroQuatY        int16
	GyroQuatZ        int16
	LTrigger         uint16
	RTrigger         uint16
	LStickX, LStickY int16
	RStickX, RStickY int16
	LPadForce        uint16
	RPadForce        uint16
	LStickForce      uint16
	RStickForce      uint16
	Frame            uint32
}

func (s *InputState) MarshalBinary() ([]byte, error) {
	return s.buildReport(s.Frame, DeckInputPayloadLen), nil
}

func (s *InputState) UnmarshalBinary(data []byte) error {
	if len(data) < InputReportLen {
		return io.ErrUnexpectedEOF
	}
	if data[0] != 0 {
		s.Frame = binary.LittleEndian.Uint32(data[4:8])
	}

	b8 := data[8]
	s.A = b8&buttonByte8A != 0
	s.X = b8&buttonByte8X != 0
	s.B = b8&buttonByte8B != 0
	s.Y = b8&buttonByte8Y != 0
	s.L1 = b8&buttonByte8L1 != 0
	s.R1 = b8&buttonByte8R1 != 0
	s.L2Digital = b8&buttonByte8L2 != 0
	s.R2Digital = b8&buttonByte8R2 != 0

	b9 := data[9]
	s.L5 = b9&buttonByte9L5 != 0
	s.Menu = b9&buttonByte9Menu != 0
	s.Steam = b9&buttonByte9Steam != 0
	s.Options = b9&buttonByte9Options != 0
	s.DPadDown = b9&buttonByte9Down != 0
	s.DPadLeft = b9&buttonByte9Left != 0
	s.DPadRight = b9&buttonByte9Right != 0
	s.DPadUp = b9&buttonByte9Up != 0

	b10 := data[10]
	s.L3 = b10&buttonByte10L3 != 0
	s.RPadTouch = b10&buttonByte10RPadTouch != 0
	s.LPadTouch = b10&buttonByte10LPadTouch != 0
	s.RPadPress = b10&buttonByte10RPadPress != 0
	s.LPadPress = b10&buttonByte10LPadPress != 0
	s.R5 = b10&buttonByte10R5 != 0

	b11 := data[11]
	s.R3 = b11&buttonByte11R3 != 0

	b13 := data[13]
	s.RStickTouch = b13&buttonByte13RStickTouch != 0
	s.LStickTouch = b13&buttonByte13LStickTouch != 0
	s.R4 = b13&buttonByte13R4 != 0
	s.L4 = b13&buttonByte13L4 != 0

	b14 := data[14]
	s.QuickAccess = b14&buttonByte14QuickAccess != 0
	s.Reserved15 = data[15]

	s.LPadX = int16(binary.LittleEndian.Uint16(data[16:18]))
	s.LPadY = int16(binary.LittleEndian.Uint16(data[18:20]))
	s.RPadX = int16(binary.LittleEndian.Uint16(data[20:22]))
	s.RPadY = int16(binary.LittleEndian.Uint16(data[22:24]))
	s.AccelX = int16(binary.LittleEndian.Uint16(data[24:26]))
	s.AccelY = int16(binary.LittleEndian.Uint16(data[26:28]))
	s.AccelZ = int16(binary.LittleEndian.Uint16(data[28:30]))
	s.Pitch = int16(binary.LittleEndian.Uint16(data[30:32]))
	s.Yaw = int16(binary.LittleEndian.Uint16(data[32:34]))
	s.Roll = int16(binary.LittleEndian.Uint16(data[34:36]))
	s.GyroQuatW = int16(binary.LittleEndian.Uint16(data[36:38]))
	s.GyroQuatX = int16(binary.LittleEndian.Uint16(data[38:40]))
	s.GyroQuatY = int16(binary.LittleEndian.Uint16(data[40:42]))
	s.GyroQuatZ = int16(binary.LittleEndian.Uint16(data[42:44]))
	s.LTrigger = binary.LittleEndian.Uint16(data[44:46])
	s.RTrigger = binary.LittleEndian.Uint16(data[46:48])
	s.LStickX = int16(binary.LittleEndian.Uint16(data[48:50]))
	s.LStickY = int16(binary.LittleEndian.Uint16(data[50:52]))
	s.RStickX = int16(binary.LittleEndian.Uint16(data[52:54]))
	s.RStickY = int16(binary.LittleEndian.Uint16(data[54:56]))
	s.LPadForce = binary.LittleEndian.Uint16(data[56:58])
	s.RPadForce = binary.LittleEndian.Uint16(data[58:60])
	s.LStickForce = binary.LittleEndian.Uint16(data[60:62])
	s.RStickForce = binary.LittleEndian.Uint16(data[62:64])
	return nil
}

// clampStickMin keeps stick Y values out of math.MinInt16. SDL3's deck driver
// negates stick Y with plain unary minus (SDL_hidapi_steamdeck.c: -sLeftStickY),
// so -32768 wraps back to -32768 and a fully deflected stick reads as the
// opposite extreme. Real deck sticks are calibrated and never report -32768.
func clampStickMin(v int16) int16 {
	if v == -32768 {
		return -32767
	}
	return v
}

func (s *InputState) buildReport(frame uint32, payloadLen byte) []byte {
	b := make([]byte, InputReportLen)
	b[0] = 0x01
	b[1] = 0x00
	b[2] = InputReportID
	b[3] = payloadLen
	binary.LittleEndian.PutUint32(b[4:8], frame)

	if s.A {
		b[8] |= buttonByte8A
	}
	if s.X {
		b[8] |= buttonByte8X
	}
	if s.B {
		b[8] |= buttonByte8B
	}
	if s.Y {
		b[8] |= buttonByte8Y
	}
	if s.L1 {
		b[8] |= buttonByte8L1
	}
	if s.R1 {
		b[8] |= buttonByte8R1
	}
	if s.L2Digital {
		b[8] |= buttonByte8L2
	}
	if s.R2Digital {
		b[8] |= buttonByte8R2
	}

	if s.L5 {
		b[9] |= buttonByte9L5
	}
	if s.Menu {
		b[9] |= buttonByte9Menu
	}
	if s.Steam {
		b[9] |= buttonByte9Steam
	}
	if s.Options {
		b[9] |= buttonByte9Options
	}
	if s.DPadDown {
		b[9] |= buttonByte9Down
	}
	if s.DPadLeft {
		b[9] |= buttonByte9Left
	}
	if s.DPadRight {
		b[9] |= buttonByte9Right
	}
	if s.DPadUp {
		b[9] |= buttonByte9Up
	}

	if s.L3 {
		b[10] |= buttonByte10L3
	}
	if s.RPadTouch {
		b[10] |= buttonByte10RPadTouch
	}
	if s.LPadTouch {
		b[10] |= buttonByte10LPadTouch
	}
	if s.RPadPress {
		b[10] |= buttonByte10RPadPress
	}
	if s.LPadPress {
		b[10] |= buttonByte10LPadPress
	}
	if s.R5 {
		b[10] |= buttonByte10R5
	}

	if s.R3 {
		b[11] |= buttonByte11R3
	}

	if s.RStickTouch {
		b[13] |= buttonByte13RStickTouch
	}
	if s.LStickTouch {
		b[13] |= buttonByte13LStickTouch
	}
	if s.R4 {
		b[13] |= buttonByte13R4
	}
	if s.L4 {
		b[13] |= buttonByte13L4
	}

	if s.QuickAccess {
		b[14] |= buttonByte14QuickAccess
	}
	b[15] = s.Reserved15

	binary.LittleEndian.PutUint16(b[16:18], uint16(s.LPadX))
	binary.LittleEndian.PutUint16(b[18:20], uint16(s.LPadY))
	binary.LittleEndian.PutUint16(b[20:22], uint16(s.RPadX))
	binary.LittleEndian.PutUint16(b[22:24], uint16(s.RPadY))
	binary.LittleEndian.PutUint16(b[24:26], uint16(s.AccelX))
	binary.LittleEndian.PutUint16(b[26:28], uint16(s.AccelY))
	binary.LittleEndian.PutUint16(b[28:30], uint16(s.AccelZ))
	binary.LittleEndian.PutUint16(b[30:32], uint16(s.Pitch))
	binary.LittleEndian.PutUint16(b[32:34], uint16(s.Yaw))
	binary.LittleEndian.PutUint16(b[34:36], uint16(s.Roll))
	gyroQuatW := s.GyroQuatW
	gyroQuatX := s.GyroQuatX
	gyroQuatY := s.GyroQuatY
	gyroQuatZ := s.GyroQuatZ
	// Do NOT force an identity quaternion when unset. We don't advertise SendOrientation
	// and never compute a live orientation, so leave the quaternion zero like InputPlumber's
	// Steam Deck target — a frozen identity made Steam ignore our raw gyro velocity.
	binary.LittleEndian.PutUint16(b[36:38], uint16(gyroQuatW))
	binary.LittleEndian.PutUint16(b[38:40], uint16(gyroQuatX))
	binary.LittleEndian.PutUint16(b[40:42], uint16(gyroQuatY))
	binary.LittleEndian.PutUint16(b[42:44], uint16(gyroQuatZ))
	binary.LittleEndian.PutUint16(b[44:46], s.LTrigger)
	binary.LittleEndian.PutUint16(b[46:48], s.RTrigger)
	binary.LittleEndian.PutUint16(b[48:50], uint16(s.LStickX))
	binary.LittleEndian.PutUint16(b[50:52], uint16(clampStickMin(s.LStickY)))
	binary.LittleEndian.PutUint16(b[52:54], uint16(s.RStickX))
	binary.LittleEndian.PutUint16(b[54:56], uint16(clampStickMin(s.RStickY)))
	binary.LittleEndian.PutUint16(b[56:58], s.LPadForce)
	binary.LittleEndian.PutUint16(b[58:60], s.RPadForce)
	binary.LittleEndian.PutUint16(b[60:62], s.LStickForce)
	binary.LittleEndian.PutUint16(b[62:64], s.RStickForce)
	return b
}

type OutputState struct {
	Data [InputReportLen]byte
}

func (o *OutputState) MarshalBinary() ([]byte, error) {
	b := make([]byte, len(o.Data))
	copy(b, o.Data[:])
	return b, nil
}

func (o *OutputState) UnmarshalBinary(data []byte) error {
	if len(data) < len(o.Data) {
		return io.ErrUnexpectedEOF
	}
	copy(o.Data[:], data[:len(o.Data)])
	return nil
}

func (o OutputState) CommandID() uint8 { return o.Data[0] }

func (o OutputState) ReportSize() uint8 { return o.Data[1] }

type RumbleCommand struct {
	EventType  uint8
	Intensity  uint8
	LeftSpeed  uint16
	RightSpeed uint16
}

func (o OutputState) AsRumble() (RumbleCommand, bool) {
	if o.Data[0] != FeatureTriggerRumbleCommand {
		return RumbleCommand{}, false
	}
	return RumbleCommand{
		EventType:  o.Data[3],
		Intensity:  o.Data[4],
		LeftSpeed:  binary.LittleEndian.Uint16(o.Data[5:7]),
		RightSpeed: binary.LittleEndian.Uint16(o.Data[7:9]),
	}, true
}

type HapticCommand struct {
	Side      uint8
	Type      uint8
	Intensity uint8
	Gain      int8
}

func (o OutputState) AsHaptic() (HapticCommand, bool) {
	if o.Data[0] != FeatureTriggerHapticCommand {
		return HapticCommand{}, false
	}
	return HapticCommand{
		Side:      o.Data[2],
		Type:      o.Data[3],
		Intensity: o.Data[4],
		Gain:      int8(o.Data[5]),
	}, true
}

type HapticPulseCommand struct {
	Side     uint8
	Duration uint16
	Interval uint16
	Count    uint16
	Gain     uint8
}

func (o OutputState) AsHapticPulse() (HapticPulseCommand, bool) {
	if o.Data[0] != FeatureTriggerHapticPulse {
		return HapticPulseCommand{}, false
	}
	return HapticPulseCommand{
		Side:     o.Data[2],
		Duration: binary.LittleEndian.Uint16(o.Data[3:5]),
		Interval: binary.LittleEndian.Uint16(o.Data[5:7]),
		Count:    binary.LittleEndian.Uint16(o.Data[7:9]),
		Gain:     o.Data[9],
	}, true
}

type PlayAudioCommand struct {
	Payload []byte
}

func (o OutputState) AsPlayAudio() (PlayAudioCommand, bool) {
	if o.Data[0] != FeaturePlayAudio {
		return PlayAudioCommand{}, false
	}
	payloadLen := int(o.Data[1])
	if payloadLen < 0 {
		payloadLen = 0
	}
	if payloadLen > len(o.Data)-2 {
		payloadLen = len(o.Data) - 2
	}
	payload := make([]byte, payloadLen)
	copy(payload, o.Data[2:2+payloadLen])
	return PlayAudioCommand{Payload: payload}, true
}
