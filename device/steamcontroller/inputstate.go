package steamcontroller

import (
	"encoding/binary"
	"io"
)

// nolint
// viiper:wire steamcontroller c2s a:bool x:bool b:bool y:bool l1:bool r1:bool menu:bool steam:bool options:bool dpadDown:bool dpadLeft:bool dpadRight:bool dpadUp:bool l3:bool lGrip:bool rGrip:bool lPadTouch:bool rPadTouch:bool lPadPress:bool rPadPress:bool lPadAndStick:bool lPadX:i16 lPadY:i16 rPadX:i16 rPadY:i16 lTrigger:u16 rTrigger:u16 lStickX:i16 lStickY:i16 accelX:i16 accelY:i16 accelZ:i16 gyroX:i16 gyroY:i16 gyroZ:i16 gyroQuatW:i16 gyroQuatX:i16 gyroQuatY:i16 gyroQuatZ:i16 batteryMv:u16
type InputState struct {
	A, X, B, Y        bool
	L1, R1            bool
	Menu              bool
	Steam             bool
	Options           bool
	DPadDown          bool
	DPadLeft          bool
	DPadRight         bool
	DPadUp            bool
	L3                bool
	LGrip             bool
	RGrip             bool
	LPadTouch         bool
	RPadTouch         bool
	LPadPress         bool
	RPadPress         bool
	LPadAndStick      bool
	LPadX, LPadY      int16
	RPadX, RPadY      int16
	LTrigger          uint16
	RTrigger          uint16
	LStickX, LStickY  int16
	AccelX, AccelY    int16
	AccelZ            int16
	GyroX, GyroY      int16
	GyroZ             int16
	GyroQuatW         int16
	GyroQuatX         int16
	GyroQuatY         int16
	GyroQuatZ         int16
	BatteryMilliVolts uint16
	Frame             uint32
}

func (s *InputState) MarshalBinary() ([]byte, error) {
	return s.buildReport(s.Frame), nil
}

func (s *InputState) UnmarshalBinary(data []byte) error {
	if len(data) < InputReportLen {
		return io.ErrUnexpectedEOF
	}

	s.Frame = binary.LittleEndian.Uint32(data[4:8])

	b8 := data[8]
	s.R1 = b8&buttonByte8R1 != 0
	s.L1 = b8&buttonByte8L1 != 0
	s.Y = b8&buttonByte8Y != 0
	s.B = b8&buttonByte8B != 0
	s.X = b8&buttonByte8X != 0
	s.A = b8&buttonByte8A != 0

	b9 := data[9]
	s.DPadUp = b9&buttonByte9Up != 0
	s.DPadRight = b9&buttonByte9Right != 0
	s.DPadLeft = b9&buttonByte9Left != 0
	s.DPadDown = b9&buttonByte9Down != 0
	s.Menu = b9&buttonByte9Menu != 0
	s.Steam = b9&buttonByte9Steam != 0
	s.Options = b9&buttonByte9Options != 0
	s.LGrip = b9&buttonByte9LGrip != 0

	b10 := data[10]
	s.RGrip = b10&buttonByte10RGrip != 0
	s.LPadPress = b10&buttonByte10LPadPress != 0
	s.RPadPress = b10&buttonByte10RPadPress != 0
	s.LPadTouch = b10&buttonByte10LPadTouch != 0
	s.RPadTouch = b10&buttonByte10RPadTouch != 0
	s.L3 = b10&buttonByte10L3 != 0
	s.LPadAndStick = b10&buttonByte10LPadAndJoy != 0

	if s.LPadTouch || s.LPadPress {
		s.LPadX = int16(binary.LittleEndian.Uint16(data[16:18]))
		s.LPadY = int16(binary.LittleEndian.Uint16(data[18:20]))
	} else {
		s.LStickX = int16(binary.LittleEndian.Uint16(data[16:18]))
		s.LStickY = int16(binary.LittleEndian.Uint16(data[18:20]))
	}
	s.RPadX = int16(binary.LittleEndian.Uint16(data[20:22]))
	s.RPadY = int16(binary.LittleEndian.Uint16(data[22:24]))
	s.LTrigger = binary.LittleEndian.Uint16(data[24:26])
	s.RTrigger = binary.LittleEndian.Uint16(data[26:28])
	s.AccelX = int16(binary.LittleEndian.Uint16(data[28:30]))
	s.AccelY = int16(binary.LittleEndian.Uint16(data[30:32]))
	s.AccelZ = int16(binary.LittleEndian.Uint16(data[32:34]))
	s.GyroX = int16(binary.LittleEndian.Uint16(data[34:36]))
	s.GyroY = int16(binary.LittleEndian.Uint16(data[36:38]))
	s.GyroZ = int16(binary.LittleEndian.Uint16(data[38:40]))
	s.GyroQuatW = int16(binary.LittleEndian.Uint16(data[40:42]))
	s.GyroQuatX = int16(binary.LittleEndian.Uint16(data[42:44]))
	s.GyroQuatY = int16(binary.LittleEndian.Uint16(data[44:46]))
	s.GyroQuatZ = int16(binary.LittleEndian.Uint16(data[46:48]))
	if raw := binary.LittleEndian.Uint16(data[50:52]); raw != 0 {
		s.LTrigger = raw
	}
	if raw := binary.LittleEndian.Uint16(data[52:54]); raw != 0 {
		s.RTrigger = raw
	}
	s.LStickX = int16(binary.LittleEndian.Uint16(data[54:56]))
	s.LStickY = int16(binary.LittleEndian.Uint16(data[56:58]))
	s.LPadX = int16(binary.LittleEndian.Uint16(data[58:60]))
	s.LPadY = int16(binary.LittleEndian.Uint16(data[60:62]))
	s.BatteryMilliVolts = binary.LittleEndian.Uint16(data[62:64])
	return nil
}

func (s *InputState) buildReport(frame uint32) []byte {
	b := make([]byte, InputReportLen)
	b[0] = 0x01
	b[1] = 0x00
	b[2] = InputReportID
	b[3] = InputPayloadLen
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
	if triggerRawToByte(s.LTrigger) == 0xff {
		b[8] |= buttonByte8L2
	}
	if triggerRawToByte(s.RTrigger) == 0xff {
		b[8] |= buttonByte8R2
	}

	if s.DPadUp {
		b[9] |= buttonByte9Up
	}
	if s.DPadRight {
		b[9] |= buttonByte9Right
	}
	if s.DPadLeft {
		b[9] |= buttonByte9Left
	}
	if s.DPadDown {
		b[9] |= buttonByte9Down
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
	if s.LGrip {
		b[9] |= buttonByte9LGrip
	}

	if s.RGrip {
		b[10] |= buttonByte10RGrip
	}
	if s.LPadPress {
		b[10] |= buttonByte10LPadPress
	}
	if s.RPadPress {
		b[10] |= buttonByte10RPadPress
	}
	if s.LPadTouch {
		b[10] |= buttonByte10LPadTouch
	}
	if s.RPadTouch {
		b[10] |= buttonByte10RPadTouch
	}
	if s.L3 {
		b[10] |= buttonByte10L3
	}
	if s.LPadAndStick {
		b[10] |= buttonByte10LPadAndJoy
	}

	primaryX := s.LStickX
	primaryY := s.LStickY
	if s.LPadTouch || s.LPadPress {
		primaryX = s.LPadX
		primaryY = s.LPadY
	}
	b[11] = triggerRawToByte(s.LTrigger)
	b[12] = triggerRawToByte(s.RTrigger)
	binary.LittleEndian.PutUint16(b[16:18], uint16(primaryX))
	binary.LittleEndian.PutUint16(b[18:20], uint16(primaryY))
	binary.LittleEndian.PutUint16(b[20:22], uint16(s.RPadX))
	binary.LittleEndian.PutUint16(b[22:24], uint16(s.RPadY))
	binary.LittleEndian.PutUint16(b[24:26], s.LTrigger)
	binary.LittleEndian.PutUint16(b[26:28], s.RTrigger)
	binary.LittleEndian.PutUint16(b[28:30], uint16(s.AccelX))
	binary.LittleEndian.PutUint16(b[30:32], uint16(s.AccelY))
	binary.LittleEndian.PutUint16(b[32:34], uint16(s.AccelZ))
	binary.LittleEndian.PutUint16(b[34:36], uint16(s.GyroX))
	binary.LittleEndian.PutUint16(b[36:38], uint16(s.GyroY))
	binary.LittleEndian.PutUint16(b[38:40], uint16(s.GyroZ))
	gyroQuatW := s.GyroQuatW
	if gyroQuatW == 0 && s.GyroQuatX == 0 && s.GyroQuatY == 0 && s.GyroQuatZ == 0 {
		gyroQuatW = 0x4000
	}
	binary.LittleEndian.PutUint16(b[40:42], uint16(gyroQuatW))
	binary.LittleEndian.PutUint16(b[42:44], uint16(s.GyroQuatX))
	binary.LittleEndian.PutUint16(b[44:46], uint16(s.GyroQuatY))
	binary.LittleEndian.PutUint16(b[46:48], uint16(s.GyroQuatZ))
	binary.LittleEndian.PutUint16(b[50:52], s.LTrigger)
	binary.LittleEndian.PutUint16(b[52:54], s.RTrigger)
	binary.LittleEndian.PutUint16(b[54:56], uint16(s.LStickX))
	binary.LittleEndian.PutUint16(b[56:58], uint16(s.LStickY))
	binary.LittleEndian.PutUint16(b[58:60], uint16(s.LPadX))
	binary.LittleEndian.PutUint16(b[60:62], uint16(s.LPadY))
	battery := s.BatteryMilliVolts
	if battery == 0 {
		battery = DefaultBatteryMilliVolts
	}
	binary.LittleEndian.PutUint16(b[62:64], battery)
	return b
}

func triggerRawToByte(raw uint16) byte {
	if raw >= MaxAnalogTriggerRaw {
		return 0xff
	}
	return byte((uint32(raw) * 0xff) / MaxAnalogTriggerRaw)
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
