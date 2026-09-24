package steamcontroller

import (
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"

	"github.com/Alia5/VIIPER/device"
	"github.com/Alia5/VIIPER/usb"
	"github.com/Alia5/VIIPER/usb/hid"
	"github.com/Alia5/VIIPER/usbip"
)

const (
	keyboardInterfaceNumber   = 0x00
	mouseInterfaceNumber      = 0x01
	controllerInterfaceNumber = 0x02

	keyboardEndpointNumber   = 0x01
	mouseEndpointNumber      = 0x02
	controllerEndpointNumber = 0x03
)

var zeroMouseReport = []byte{0x00, 0x00, 0x00, 0x00}

var zeroKeyboardReport = []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

// These settings represent the controller's baseline firmware state before Steam
// applies its open-time reset sequence.
var firmwareDefaultSettings = map[uint8]uint16{
	SettingLeftTrackpadMode:    TrackpadModeNone,
	SettingRightTrackpadMode:   TrackpadModeAbsoluteMouse,
	SettingLizardMode:          LizardModeOn,
	SettingSmoothAbsoluteMouse: 1,
	SettingEnableRawJoystick:   0,
	SettingEnableFastScan:      0,
	SettingIMUMode:             GyroModeOff,
	SettingWirelessPacketVer:   0,
}

// Steam programs a smaller runtime delta after loading defaults.
var steamRuntimeSettings = map[uint8]uint16{
	SettingLeftTrackpadMode:    TrackpadModeNone,
	SettingRightTrackpadMode:   TrackpadModeNone,
	SettingLizardMode:          LizardModeOn,
	SettingSmoothAbsoluteMouse: 0,
	SettingEnableRawJoystick:   0,
	SettingEnableFastScan:      0,
	SettingIMUMode:             GyroModeOff,
	SettingWirelessPacketVer:   2,
}

const (
	hidKeyEscape = 0x29
	hidKeyEnter  = 0x28
	hidKeyRight  = 0x4f
	hidKeyLeft   = 0x50
	hidKeyDown   = 0x51
	hidKeyUp     = 0x52
)

type SteamController struct {
	gate                *device.InputGate
	inputState          *InputState
	stateMu             sync.Mutex
	featureMu           sync.Mutex
	outputFunc          func(OutputState)
	frame               uint32
	descriptor          usb.Descriptor
	controller          controllerState
	imuBias             imuBiasState
	lastFeatureResponse []byte
}

type controllerState struct {
	mode         byte
	digitalMaps  bool
	settings     map[uint8]uint16
	boardSerial  string
	unitSerial   string
	uniqueID     uint32
	boardRev     uint32
	firmwareTime uint32
}

type imuBiasState struct {
	valid          bool
	accelX, accelY int16
	accelZ         int16
	gyroX, gyroY   int16
	gyroZ          int16
}

var maxSettings = map[uint8]uint16{
	SettingLeftTrackpadMode:    TrackpadModeNone,
	SettingRightTrackpadMode:   TrackpadModeNone,
	SettingLizardMode:          LizardModeOn,
	SettingSmoothAbsoluteMouse: 1,
	SettingIMUMode:             GyroModeSteering | GyroModeTilt | GyroModeSendOrientation | GyroModeSendRawAccel | GyroModeSendRawGyro,
	SettingEnableRawJoystick:   1,
	SettingEnableFastScan:      1,
	SettingWirelessPacketVer:   2,
}

var settingsOrder = []uint8{
	SettingLeftTrackpadMode,
	SettingRightTrackpadMode,
	SettingLizardMode,
	SettingSmoothAbsoluteMouse,
	SettingEnableRawJoystick,
	SettingEnableFastScan,
	SettingIMUMode,
	SettingWirelessPacketVer,
}

func newControllerState() controllerState {
	return controllerState{
		settings:     cloneSettings(firmwareDefaultSettings),
		boardSerial:  "SteamController-0001",
		unitSerial:   "SteamController-0001",
		uniqueID:     0x53544354,
		boardRev:     1,
		firmwareTime: 0x57bf5c10,
		mode:         byte(firmwareDefaultSettings[SettingLizardMode]),
		digitalMaps:  true,
	}
}

func New(o *device.CreateOptions) (*SteamController, error) {
	d := &SteamController{
		gate:       device.NewInputGate(),
		descriptor: defaultDescriptor,
		inputState: &InputState{},
		controller: newControllerState(),
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

func (d *SteamController) lizardModeEnabled() bool {
	return d.controller.digitalMaps && d.controller.settings[SettingLizardMode] != uint16(LizardModeOff)
}

func (d *SteamController) imuMode() uint16 {
	return d.controller.settings[SettingIMUMode]
}

func (d *SteamController) reportedControllerMode() byte {
	if d.lizardModeEnabled() {
		return byte(LizardModeOn)
	}
	return byte(LizardModeOff)
}

func (d *SteamController) SetOutputCallback(f func(OutputState)) {
	d.outputFunc = f
}

func (d *SteamController) UpdateInputState(state *InputState) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if state == nil {
		d.inputState = &InputState{}
		d.gate.Signal()
		return
	}
	st := *state
	st.Frame = atomic.AddUint32(&d.frame, 1)
	d.inputState = &st
	d.gate.Signal()
}

func (d *SteamController) buildInputReport(st InputState, frame uint32) []byte {
	if d.imuBias.valid {
		st.AccelX -= d.imuBias.accelX
		st.AccelY -= d.imuBias.accelY
		st.AccelZ -= d.imuBias.accelZ
		st.GyroX -= d.imuBias.gyroX
		st.GyroY -= d.imuBias.gyroY
		st.GyroZ -= d.imuBias.gyroZ
	}
	report := st.buildReport(frame)
	imuMode := d.imuMode()
	if imuMode&GyroModeSendRawAccel == 0 {
		copy(report[28:34], []byte{0, 0, 0, 0, 0, 0})
	}
	if imuMode&GyroModeSendRawGyro == 0 {
		copy(report[34:40], []byte{0, 0, 0, 0, 0, 0})
	}
	if imuMode&GyroModeSendOrientation == 0 {
		copy(report[40:48], []byte{0, 0, 0, 0, 0, 0, 0, 0})
	}
	return report
}

func (d *SteamController) HandleTransfer(ctx context.Context, ep uint32, dir uint32, out []byte) []byte {
	if dir == usbip.DirIn {
		switch ep {
		case mouseEndpointNumber:
			if device.GateCancelled == device.BlockUntilDeadline(ctx) {
				return nil
			}
			return append([]byte(nil), zeroMouseReport...)
		case keyboardEndpointNumber:
			if device.GateCancelled == device.BlockUntilDeadline(ctx) {
				return nil
			}
			d.stateMu.Lock()
			st := *d.inputState
			d.stateMu.Unlock()
			return d.buildLizardKeyboardReport(st)
		case controllerEndpointNumber:
			if device.GateCancelled == d.gate.Wait(ctx) {
				return nil
			}
			d.stateMu.Lock()
			st := *d.inputState
			d.stateMu.Unlock()
			report := d.buildInputReport(st, st.Frame)
			return report
		default:
			return nil
		}
	}
	if dir == usbip.DirOut && ep == controllerEndpointNumber {
		d.handleHostCommand(out)
	}
	return nil
}

func (d *SteamController) buildLizardKeyboardReport(st InputState) []byte {
	if !d.lizardKeyboardEnabled() {
		return append([]byte(nil), zeroKeyboardReport...)
	}

	report := make([]byte, len(zeroKeyboardReport))
	keys := make([]byte, 0, 6)
	appendKey := func(key byte, active bool) {
		if !active || len(keys) >= 6 {
			return
		}
		keys = append(keys, key)
	}

	appendKey(hidKeyEnter, st.A)
	appendKey(hidKeyEscape, st.B)
	appendKey(hidKeyUp, st.DPadUp)
	appendKey(hidKeyDown, st.DPadDown)
	appendKey(hidKeyLeft, st.DPadLeft)
	appendKey(hidKeyRight, st.DPadRight)

	copy(report[2:], keys)
	return report
}

func (d *SteamController) lizardKeyboardEnabled() bool {
	if !d.controller.digitalMaps {
		return false
	}
	return d.lizardModeEnabled()
}

func (d *SteamController) HandleControl(bmRequestType, bRequest uint8, wValue, wIndex, wLength uint16, data []byte) ([]byte, bool) {
	const (
		hidGetReport = 0x01
		hidSetReport = 0x09

		reportTypeInput   = 0x01
		reportTypeOutput  = 0x02
		reportTypeFeature = 0x03
	)

	reportType := uint8(wValue >> 8)
	reportID := uint8(wValue & 0xff)
	iface := uint8(wIndex & 0xff)
	if iface != controllerInterfaceNumber {
		return nil, false
	}

	if bmRequestType == 0xa1 && bRequest == hidGetReport {
		switch reportType {
		case reportTypeInput:
			d.stateMu.Lock()
			st := *d.inputState
			d.stateMu.Unlock()
			report := d.buildInputReport(st, st.Frame)
			if wLength > 0 && int(wLength) < len(report) {
				return report[:wLength], true
			}
			return report, true
		case reportTypeFeature:
			resp := d.getFeatureResponse(reportID)
			if resp == nil {
				return nil, false
			}
			if wLength > 0 && int(wLength) < len(resp) {
				return resp[:wLength], true
			}
			return resp, true
		}
	}

	if bmRequestType == 0x21 && bRequest == hidSetReport {
		if reportType == reportTypeOutput || reportType == reportTypeFeature {
			data = normalizeHostCommand(data, reportID)
			if reportType == reportTypeFeature {
				d.setFeatureResponse(data)
			}
			d.handleHostCommand(data)
			return nil, true
		}
	}

	return nil, false
}

func normalizeHostCommand(data []byte, reportID uint8) []byte {
	if len(data) == 0 && reportID != 0 {
		return []byte{reportID}
	}
	if len(data) > 1 && data[0] == 0x00 {
		return append([]byte(nil), data[1:]...)
	}
	return append([]byte(nil), data...)
}

func (d *SteamController) handleHostCommand(data []byte) {
	if len(data) == 0 {
		return
	}
	if len(data) > 1 && data[0] == 0x00 {
		data = data[1:]
	}
	switch data[0] {
	case FeatureSetControllerMode:
		if len(data) >= 3 {
			d.controller.mode = data[2]
		}
	case FeatureSetSettingsValues:
		d.applySettings(data)
	case FeatureLoadDefaultSettings:
		d.resetSettings()
	case FeatureFactoryReset:
		d.controller = newControllerState()
		d.imuBias = imuBiasState{}
	case FeatureClearSettingsValues:
		d.resetSettings()
	case FeatureClearDigitalMappings:
		d.controller.digitalMaps = false
	case FeatureSetDefaultMappings:
		d.controller.digitalMaps = true
	case FeatureSetDigitalMappings:
		d.controller.digitalMaps = true
	case FeatureResetIMU:
		d.stateMu.Lock()
		st := *d.inputState
		d.stateMu.Unlock()
		d.imuBias = imuBiasState{
			valid:  true,
			accelX: st.AccelX,
			accelY: st.AccelY,
			accelZ: st.AccelZ,
			gyroX:  st.GyroX,
			gyroY:  st.GyroY,
			gyroZ:  st.GyroZ,
		}
	}
	if d.outputFunc == nil {
		return
	}
	var out OutputState
	copy(out.Data[:], data)
	d.outputFunc(out)
}

func cloneSettings(src map[uint8]uint16) map[uint8]uint16 {
	dst := make(map[uint8]uint16, len(src))
	for setting, value := range src {
		dst[setting] = value
	}
	return dst
}

func (d *SteamController) resetSettings() {
	state := newControllerState()
	d.controller.settings = cloneSettings(state.settings)
	d.controller.mode = state.mode
	d.imuBias = imuBiasState{}
}

func (d *SteamController) applySettings(data []byte) {
	if len(data) < 2 {
		return
	}
	payloadLen := int(data[1])
	if payloadLen > len(data)-2 {
		payloadLen = len(data) - 2
	}
	for offset := 2; offset+2 < 2+payloadLen; offset += 3 {
		setting := data[offset]
		value := binary.LittleEndian.Uint16(data[offset+1 : offset+3])
		d.controller.settings[setting] = value
		if setting == SettingLizardMode {
			d.controller.mode = byte(value)
		}
	}
}

func (d *SteamController) getFeatureResponse(reportID uint8) []byte {
	if reportID != 0 {
		return d.featureResponse([]byte{reportID})
	}

	d.featureMu.Lock()
	defer d.featureMu.Unlock()
	if len(d.lastFeatureResponse) == 0 {
		return nil
	}
	return append([]byte(nil), d.lastFeatureResponse...)
}

func (d *SteamController) setFeatureResponse(request []byte) {
	resp := d.featureResponse(request)
	d.featureMu.Lock()
	defer d.featureMu.Unlock()
	if resp == nil {
		d.lastFeatureResponse = nil
		return
	}
	d.lastFeatureResponse = append([]byte(nil), resp...)
}

func (d *SteamController) featureResponse(request []byte) []byte {
	if len(request) == 0 {
		return nil
	}
	command := request[0]
	resp := make([]byte, InputReportLen)
	resp[0] = command
	switch command {
	case FeatureSetSettingsValues, FeatureClearDigitalMappings, FeatureSetDefaultMappings, FeatureSetDigitalMappings,
		FeatureClearSettingsValues, FeatureSetControllerMode, FeatureLoadDefaultSettings, FeatureFactoryReset, FeatureResetIMU:
		copy(resp, request)
		return resp
	case FeatureGetDeviceInfo:
		resp[1] = byte(14 + len("Wired Controller"))
		binary.LittleEndian.PutUint16(resp[4:6], d.descriptor.Device.IDVendor)
		binary.LittleEndian.PutUint16(resp[6:8], d.descriptor.Device.IDProduct)
		resp[8] = 0x01
		resp[9] = d.reportedControllerMode()
		copy(resp[16:], []byte("Wired Controller"))
		return resp
	case FeatureGetChipID:
		resp[1] = 14
		copy(resp[4:], []byte("STEAMCTRL-0001"))
		return resp
	case FeatureGetAttributesValues:
		resp[1] = d.fillAttributes(resp[2:])
		return resp
	case FeatureGetStringAttribute:
		resp[1] = d.fillStringAttribute(resp[2:], request)
		return resp
	case FeatureGetDigitalMappings:
		resp[1] = d.fillDigitalMappings(resp[2:])
		return resp
	case FeatureGetSettingsValues:
		resp[1] = fillSettings(resp[2:], d.controller.settings)
		return resp
	case FeatureGetSettingsDefaults:
		resp[1] = fillSettings(resp[2:], firmwareDefaultSettings)
		return resp
	case FeatureGetSettingsMaxs:
		resp[1] = fillSettings(resp[2:], maxSettings)
		return resp
	default:
		return nil
	}
}

func (d *SteamController) fillDigitalMappings(buf []byte) byte {
	if len(buf) == 0 {
		return 0
	}
	if !d.controller.digitalMaps {
		buf[0] = 0xff
		return 1
	}
	buf[0] = 0x00
	return 1
}

func fillSettings(buf []byte, settings map[uint8]uint16) byte {
	offset := 0
	for _, setting := range settingsOrder {
		value, ok := settings[setting]
		if !ok || offset+3 > len(buf) {
			continue
		}
		buf[offset] = setting
		binary.LittleEndian.PutUint16(buf[offset+1:offset+3], value)
		offset += 3
	}
	return byte(offset)
}

func (d *SteamController) fillAttributes(buf []byte) byte {
	entries := []struct {
		tag   byte
		value uint32
	}{
		{tag: AttributeUniqueID, value: d.controller.uniqueID},
		{tag: AttributeProductID, value: uint32(d.descriptor.Device.IDProduct)},
		{tag: AttributeCapabilities, value: CapabilityAll},
		{tag: AttributeBoardRevision, value: d.controller.boardRev},
		{tag: AttributeFirmwareBuildTime, value: d.controller.firmwareTime},
		{tag: AttributeConnectionIntervalUs, value: 9000},
	}

	offset := 0
	for _, entry := range entries {
		if offset+5 > len(buf) {
			break
		}
		buf[offset] = entry.tag
		binary.LittleEndian.PutUint32(buf[offset+1:offset+5], entry.value)
		offset += 5
	}
	return byte(offset)
}

func (d *SteamController) fillStringAttribute(buf []byte, request []byte) byte {
	if len(buf) < 2 {
		return 0
	}
	attribute := byte(StringAttributeBoardSerial)
	if len(request) >= 3 {
		attribute = request[2]
	}
	buf[0] = attribute

	value := d.controller.boardSerial
	if attribute == byte(StringAttributeUnitSerial) {
		value = d.controller.unitSerial
	}
	count := copy(buf[1:], []byte(value))
	return byte(count)
}

func (d *SteamController) GetDescriptor() *usb.Descriptor {
	return &d.descriptor
}

func (d *SteamController) GetDeviceSpecificArgs() map[string]any {
	return map[string]any{}
}

var reportDescriptor = hid.ReportDescriptor{
	Items: []hid.Item{
		hid.UsagePage{Page: 0xff00},
		hid.Usage{Usage: 0x01},
		hid.Collection{Kind: hid.CollectionApplication, Items: []hid.Item{
			hid.LogicalMinimum{Min: 0},
			hid.LogicalMaximum{Max: 255},
			hid.ReportSize{Bits: 8},
			hid.ReportCount{Count: 64},
			hid.Usage{Usage: 0x01},
			hid.Input{Flags: hid.MainData | hid.MainVar | hid.MainAbs},
			hid.ReportCount{Count: 64},
			hid.Usage{Usage: 0x01},
			hid.Output{Flags: hid.MainData | hid.MainVar | hid.MainAbs},
			hid.ReportCount{Count: 64},
			hid.Usage{Usage: 0x01},
			hid.Feature{Flags: hid.MainData | hid.MainVar | hid.MainAbs},
		}},
	},
}

var mouseReportDescriptor = hid.ReportDescriptor{
	Items: []hid.Item{
		hid.UsagePage{Page: hid.UsagePageGenericDesktop},
		hid.Usage{Usage: hid.UsageMouse},
		hid.Collection{Kind: hid.CollectionApplication, Items: []hid.Item{
			hid.Usage{Usage: hid.UsagePointer},
			hid.Collection{Kind: hid.CollectionPhysical, Items: []hid.Item{
				hid.UsagePage{Page: hid.UsagePageButton},
				hid.UsageMinimum{Min: 0x01},
				hid.UsageMaximum{Max: 0x05},
				hid.LogicalMinimum{Min: 0},
				hid.LogicalMaximum{Max: 1},
				hid.ReportCount{Count: 5},
				hid.ReportSize{Bits: 1},
				hid.Input{Flags: hid.MainData | hid.MainVar | hid.MainAbs},
				hid.ReportCount{Count: 1},
				hid.ReportSize{Bits: 3},
				hid.Input{Flags: hid.MainConst},
				hid.UsagePage{Page: hid.UsagePageGenericDesktop},
				hid.Usage{Usage: hid.UsageX},
				hid.Usage{Usage: hid.UsageY},
				hid.Usage{Usage: hid.UsageWheel},
				hid.LogicalMinimum{Min: -127},
				hid.LogicalMaximum{Max: 127},
				hid.ReportSize{Bits: 8},
				hid.ReportCount{Count: 3},
				hid.Input{Flags: hid.MainData | hid.MainVar | hid.MainRel},
			}},
		}},
	},
}

var keyboardReportDescriptor = hid.ReportDescriptor{
	Items: []hid.Item{
		hid.UsagePage{Page: hid.UsagePageGenericDesktop},
		hid.Usage{Usage: hid.UsageKeyboard},
		hid.Collection{Kind: hid.CollectionApplication, Items: []hid.Item{
			hid.UsagePage{Page: hid.UsagePageKeyboard},
			hid.UsageMinimum{Min: 0xE0},
			hid.UsageMaximum{Max: 0xE7},
			hid.LogicalMinimum{Min: 0},
			hid.LogicalMaximum{Max: 1},
			hid.ReportSize{Bits: 1},
			hid.ReportCount{Count: 8},
			hid.Input{Flags: hid.MainData | hid.MainVar | hid.MainAbs},
			hid.ReportSize{Bits: 8},
			hid.ReportCount{Count: 1},
			hid.Input{Flags: hid.MainConst},
			hid.ReportSize{Bits: 8},
			hid.ReportCount{Count: 6},
			hid.LogicalMinimum{Min: 0},
			hid.LogicalMaximum{Max: 255},
			hid.UsageMinimum{Min: 0x00},
			hid.UsageMaximum{Max: 0xFF},
			hid.Input{Flags: hid.MainData | hid.MainArray},
			hid.UsagePage{Page: hid.UsagePageLEDs},
			hid.UsageMinimum{Min: 0x01},
			hid.UsageMaximum{Max: 0x05},
			hid.LogicalMinimum{Min: 0},
			hid.LogicalMaximum{Max: 1},
			hid.ReportCount{Count: 5},
			hid.ReportSize{Bits: 1},
			hid.Output{Flags: hid.MainData | hid.MainVar | hid.MainAbs},
			hid.ReportCount{Count: 1},
			hid.ReportSize{Bits: 3},
			hid.Output{Flags: hid.MainConst},
		}},
	},
}

func makeHIDFunction(report hid.ReportDescriptor) *usb.HIDFunction {
	return &usb.HIDFunction{
		Descriptor: usb.HIDDescriptor{
			BcdHID:       0x0111,
			BCountryCode: 0x00,
			Descriptors:  []usb.HIDSubDescriptor{{Type: usb.ReportDescType}},
		},
		ReportDescriptor: report,
	}
}

var defaultDescriptor = usb.Descriptor{
	Device: usb.DeviceDescriptor{
		BcdUSB:             0x0200,
		BDeviceClass:       0x00,
		BDeviceSubClass:    0x00,
		BDeviceProtocol:    0x00,
		BMaxPacketSize0:    0x40,
		IDVendor:           DefaultVID,
		IDProduct:          DefaultPID,
		BcdDevice:          0x0100,
		IManufacturer:      0x01,
		IProduct:           0x02,
		ISerialNumber:      0x00,
		BNumConfigurations: 0x01,
		Speed:              2,
	},
	Configuration: usb.ConfigurationDescriptor{
		BConfigurationValue: 0x01,
		BMAttributes:        0xa0,
		BMaxPower:           250,
	},
	Interfaces: []usb.InterfaceConfig{
		{
			Descriptor: usb.InterfaceDescriptor{
				BInterfaceNumber:   keyboardInterfaceNumber,
				BAlternateSetting:  0x00,
				BNumEndpoints:      0x01,
				BInterfaceClass:    0x03,
				BInterfaceSubClass: 0x01,
				BInterfaceProtocol: 0x01,
				IInterface:         0x03,
			},
			HID: makeHIDFunction(keyboardReportDescriptor),
			Endpoints: []usb.EndpointDescriptor{{
				BEndpointAddress: 0x80 | keyboardEndpointNumber,
				BMAttributes:     0x03,
				WMaxPacketSize:   0x0008,
				BInterval:        0x0a,
			}},
		},
		{
			Descriptor: usb.InterfaceDescriptor{
				BInterfaceNumber:   mouseInterfaceNumber,
				BAlternateSetting:  0x00,
				BNumEndpoints:      0x01,
				BInterfaceClass:    0x03,
				BInterfaceSubClass: 0x00,
				BInterfaceProtocol: 0x02,
				IInterface:         0x04,
			},
			HID: makeHIDFunction(mouseReportDescriptor),
			Endpoints: []usb.EndpointDescriptor{{
				BEndpointAddress: 0x80 | mouseEndpointNumber,
				BMAttributes:     0x03,
				WMaxPacketSize:   0x0004,
				BInterval:        0x06,
			}},
		},
		{
			Descriptor: usb.InterfaceDescriptor{
				BInterfaceNumber:   controllerInterfaceNumber,
				BAlternateSetting:  0x00,
				BNumEndpoints:      0x01,
				BInterfaceClass:    0x03,
				BInterfaceSubClass: 0x00,
				BInterfaceProtocol: 0x00,
				IInterface:         0x05,
			},
			HID: makeHIDFunction(reportDescriptor),
			Endpoints: []usb.EndpointDescriptor{{
				BEndpointAddress: 0x80 | controllerEndpointNumber,
				BMAttributes:     0x03,
				WMaxPacketSize:   0x0040,
				BInterval:        0x06,
			}},
		},
	},
	Strings: map[uint8]string{
		0: "\x04\x09",
		1: "Valve Software",
		2: "Wired Controller",
		3: "Keyboard",
		4: "Mouse",
		5: "Valve",
	},
}
