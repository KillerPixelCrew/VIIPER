package steamcontroller_test

import (
	"context"
	"encoding/binary"
	"io"
	"testing"
	"time"

	viiperTesting "github.com/Alia5/VIIPER/_testing"
	"github.com/Alia5/VIIPER/device/steamcontroller"
	"github.com/Alia5/VIIPER/internal/server/api"
	"github.com/Alia5/VIIPER/internal/server/api/handler"
	"github.com/Alia5/VIIPER/usbip"
	"github.com/Alia5/VIIPER/viiperclient"
	"github.com/Alia5/VIIPER/virtualbus"
	"github.com/stretchr/testify/assert"

	_ "github.com/Alia5/VIIPER/internal/registry"
)

const steamControllerEndpoint = 3
const steamControllerInterface = 2
const steamControllerKeyboardEndpoint = 1

func TestInputReports(t *testing.T) {
	tests := []struct {
		name     string
		state    steamcontroller.InputState
		validate func(t *testing.T, got []byte)
	}{
		{
			name:  "neutral defaults",
			state: steamcontroller.InputState{},
			validate: func(t *testing.T, got []byte) {
				assert.Len(t, got, steamcontroller.InputReportLen)
				assert.Equal(t, byte(0x01), got[0])
				assert.Equal(t, byte(0x00), got[1])
				assert.Equal(t, byte(steamcontroller.InputReportID), got[2])
				assert.Equal(t, byte(steamcontroller.InputPayloadLen), got[3])
				assert.Equal(t, make([]byte, 3), got[8:11])
				assert.Equal(t, make([]byte, 20), got[28:48])
				assert.Equal(t, uint16(steamcontroller.DefaultBatteryMilliVolts), binary.LittleEndian.Uint16(got[62:64]))
			},
		},
		{
			name: "buttons and axes",
			state: steamcontroller.InputState{
				A: true, Y: true, L1: true,
				Menu: true, Steam: true, DPadLeft: true,
				LGrip: true, RGrip: true, L3: true,
				LPadTouch: true, LPadPress: true, RPadTouch: true,
				LPadX: -1000, LPadY: 2000,
				RPadX: 3000, RPadY: -4000,
				LTrigger: 1234, RTrigger: 4321,
				LStickX: -2222, LStickY: 3333,
			},
			validate: func(t *testing.T, got []byte) {
				assert.Equal(t, byte(0x98), got[8])
				assert.Equal(t, byte(0xb4), got[9])
				assert.Equal(t, byte(0x5b), got[10])
				assert.Equal(t, byte(12), got[11])
				assert.Equal(t, byte(42), got[12])
				assert.Equal(t, []byte{0x00, 0x00, 0x00}, got[13:16])
				assert.Equal(t, uint16(0xfc18), binary.LittleEndian.Uint16(got[16:18]))
				assert.Equal(t, uint16(2000), binary.LittleEndian.Uint16(got[18:20]))
				assert.Equal(t, uint16(3000), binary.LittleEndian.Uint16(got[20:22]))
				assert.Equal(t, uint16(0xf060), binary.LittleEndian.Uint16(got[22:24]))
				assert.Equal(t, uint16(1234), binary.LittleEndian.Uint16(got[24:26]))
				assert.Equal(t, uint16(4321), binary.LittleEndian.Uint16(got[26:28]))
				assert.Equal(t, make([]byte, 20), got[28:48])
				assert.Equal(t, uint16(0xf752), binary.LittleEndian.Uint16(got[54:56]))
				assert.Equal(t, uint16(3333), binary.LittleEndian.Uint16(got[56:58]))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev, err := steamcontroller.New(nil)
			if !assert.NoError(t, err) {
				return
			}
			dev.UpdateInputState(&tt.state)
			got := dev.HandleTransfer(gateTestCtx(), steamControllerEndpoint, usbip.DirIn, nil)
			tt.validate(t, got)
		})
	}
}

func TestLizardKeyboardReports(t *testing.T) {
	dev, err := steamcontroller.New(nil)
	if !assert.NoError(t, err) {
		return
	}

	dev.UpdateInputState(&steamcontroller.InputState{
		A:         true,
		B:         true,
		DPadUp:    true,
		DPadRight: true,
	})

	report := dev.HandleTransfer(gateTestCtx(), steamControllerKeyboardEndpoint, usbip.DirIn, nil)
	if !assert.Len(t, report, 8) {
		return
	}
	assert.Equal(t, []byte{0x00, 0x00, 0x28, 0x29, 0x52, 0x4f, 0x00, 0x00}, report)

	_, handled := dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureClearDigitalMappings})
	if !assert.True(t, handled) {
		return
	}
	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureGetDeviceInfo})
	if !assert.True(t, handled) {
		return
	}
	resp, handled := dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.LizardModeOff), resp[9])
	assert.Equal(t, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, dev.HandleTransfer(gateTestCtx(), steamControllerKeyboardEndpoint, usbip.DirIn, nil))
	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 4, []byte{0x00, steamcontroller.FeatureGetDigitalMappings, 0x01, 0x00})
	if !assert.True(t, handled) {
		return
	}
	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.FeatureGetDigitalMappings), resp[0])
	assert.Equal(t, byte(0x01), resp[1])
	assert.Equal(t, byte(0xff), resp[2])

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureSetDefaultMappings})
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, []byte{0x00, 0x00, 0x28, 0x29, 0x52, 0x4f, 0x00, 0x00}, dev.HandleTransfer(gateTestCtx(), steamControllerKeyboardEndpoint, usbip.DirIn, nil))
	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 4, []byte{0x00, steamcontroller.FeatureGetDigitalMappings, 0x01, 0x00})
	if !assert.True(t, handled) {
		return
	}
	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.FeatureGetDigitalMappings), resp[0])
	assert.Equal(t, byte(0x01), resp[1])
	assert.Equal(t, byte(0x00), resp[2])

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 6, []byte{0x00, steamcontroller.FeatureSetSettingsValues, 0x03, steamcontroller.SettingLizardMode, byte(steamcontroller.LizardModeOff), 0x00})
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, dev.HandleTransfer(gateTestCtx(), steamControllerKeyboardEndpoint, usbip.DirIn, nil))

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 6, []byte{0x00, steamcontroller.FeatureSetSettingsValues, 0x03, steamcontroller.SettingLizardMode, byte(steamcontroller.LizardModeOn), 0x00})
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, []byte{0x00, 0x00, 0x28, 0x29, 0x52, 0x4f, 0x00, 0x00}, dev.HandleTransfer(gateTestCtx(), steamControllerKeyboardEndpoint, usbip.DirIn, nil))

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 4, []byte{0x00, steamcontroller.FeatureSetControllerMode, 0x01, 0x01})
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, []byte{0x00, 0x00, 0x28, 0x29, 0x52, 0x4f, 0x00, 0x00}, dev.HandleTransfer(gateTestCtx(), steamControllerKeyboardEndpoint, usbip.DirIn, nil))
}

func TestWriteRegisterGyroMode(t *testing.T) {
	dev, err := steamcontroller.New(nil)
	if !assert.NoError(t, err) {
		return
	}

	request := []byte{0x00, steamcontroller.FeatureSetSettingsValues, 0x03, steamcontroller.SettingIMUMode, steamcontroller.GyroModeSendRawAccel | steamcontroller.GyroModeSendRawGyro, 0x00}
	_, handled := dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, uint16(len(request)), request)
	if !assert.True(t, handled) {
		return
	}

	resp, handled := dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.FeatureSetSettingsValues), resp[0])
	assert.Equal(t, byte(0x03), resp[1])
	assert.Equal(t, byte(steamcontroller.SettingIMUMode), resp[2])
	assert.Equal(t, byte(steamcontroller.GyroModeSendRawAccel|steamcontroller.GyroModeSendRawGyro), resp[3])
	assert.Equal(t, byte(0x00), resp[4])

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureGetSettingsValues})
	if !assert.True(t, handled) {
		return
	}
	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	settings := resp[2 : 2+int(resp[1])]
	var foundIMUMode bool
	for index := 0; index+2 < len(settings); index += 3 {
		if settings[index] == steamcontroller.SettingIMUMode {
			foundIMUMode = true
			assert.Equal(t, uint16(steamcontroller.GyroModeSendRawAccel|steamcontroller.GyroModeSendRawGyro), binary.LittleEndian.Uint16(settings[index+1:index+3]))
			break
		}
	}
	assert.True(t, foundIMUMode)

	dev.UpdateInputState(&steamcontroller.InputState{
		AccelX:    111,
		AccelY:    -222,
		AccelZ:    333,
		GyroX:     444,
		GyroY:     -555,
		GyroZ:     666,
		GyroQuatW: 0x1234,
		GyroQuatX: 0x2345,
		GyroQuatY: 0x3456,
		GyroQuatZ: 0x4567,
	})

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureResetIMU})
	if !assert.True(t, handled) {
		return
	}
	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.FeatureResetIMU), resp[0])

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureGetSettingsValues})
	if !assert.True(t, handled) {
		return
	}
	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	settings = resp[2 : 2+int(resp[1])]
	for index := 0; index+2 < len(settings); index += 3 {
		if settings[index] == steamcontroller.SettingIMUMode {
			assert.Equal(t, uint16(steamcontroller.GyroModeSendRawAccel|steamcontroller.GyroModeSendRawGyro), binary.LittleEndian.Uint16(settings[index+1:index+3]))
			report := dev.HandleTransfer(gateTestCtx(), steamControllerEndpoint, usbip.DirIn, nil)
			assert.Equal(t, make([]byte, 12), report[28:40])
			assert.Equal(t, make([]byte, 8), report[40:48])

			dev.UpdateInputState(&steamcontroller.InputState{
				AccelX:    222,
				AccelY:    -444,
				AccelZ:    666,
				GyroX:     888,
				GyroY:     -1110,
				GyroZ:     1332,
				GyroQuatW: 0x1234,
				GyroQuatX: 0x2345,
				GyroQuatY: 0x3456,
				GyroQuatZ: 0x4567,
			})
			report = dev.HandleTransfer(gateTestCtx(), steamControllerEndpoint, usbip.DirIn, nil)
			assert.Equal(t, uint16(111), binary.LittleEndian.Uint16(report[28:30]))
			assert.Equal(t, uint16(0xff22), binary.LittleEndian.Uint16(report[30:32]))
			assert.Equal(t, uint16(333), binary.LittleEndian.Uint16(report[32:34]))
			assert.Equal(t, uint16(444), binary.LittleEndian.Uint16(report[34:36]))
			assert.Equal(t, uint16(0xfdd5), binary.LittleEndian.Uint16(report[36:38]))
			assert.Equal(t, uint16(666), binary.LittleEndian.Uint16(report[38:40]))
			assert.Equal(t, make([]byte, 8), report[40:48])
			return
		}
	}
	t.Fatalf("imu mode setting not found in response: %v", settings)
}

func TestFeatureResponses(t *testing.T) {
	dev, err := steamcontroller.New(nil)
	if !assert.NoError(t, err) {
		return
	}
	desc := dev.GetDescriptor()
	if !assert.Len(t, desc.Interfaces, 3) {
		return
	}
	assert.Equal(t, uint8(0), desc.Interfaces[0].Descriptor.BInterfaceNumber)
	assert.Equal(t, uint8(1), desc.Interfaces[1].Descriptor.BInterfaceNumber)
	assert.Equal(t, uint8(2), desc.Interfaces[2].Descriptor.BInterfaceNumber)

	_, handled := dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureGetAttributesValues})
	if !assert.True(t, handled) {
		return
	}

	resp, handled := dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.FeatureGetAttributesValues), resp[0])
	assert.Equal(t, byte(steamcontroller.AttributeProductID), resp[7])
	assert.Equal(t, uint32(steamcontroller.DefaultPID), binary.LittleEndian.Uint32(resp[8:12]))
	assert.Equal(t, byte(steamcontroller.AttributeCapabilities), resp[12])
	assert.Equal(t, uint32(steamcontroller.CapabilityAll), binary.LittleEndian.Uint32(resp[13:17]))
	assert.Equal(t, byte(steamcontroller.AttributeBoardRevision), resp[17])
	assert.Equal(t, uint32(1), binary.LittleEndian.Uint32(resp[18:22]))
	assert.Equal(t, byte(steamcontroller.AttributeFirmwareBuildTime), resp[22])
	assert.Equal(t, uint32(0x57bf5c10), binary.LittleEndian.Uint32(resp[23:27]))
	assert.Equal(t, byte(steamcontroller.AttributeConnectionIntervalUs), resp[27])
	assert.Equal(t, uint32(9000), binary.LittleEndian.Uint32(resp[28:32]))

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureGetDeviceInfo})
	if !assert.True(t, handled) {
		return
	}
	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.FeatureGetDeviceInfo), resp[0])
	assert.Equal(t, byte(14+len("Wired Controller")), resp[1])
	assert.Equal(t, uint16(steamcontroller.DefaultVID), binary.LittleEndian.Uint16(resp[4:6]))
	assert.Equal(t, uint16(steamcontroller.DefaultPID), binary.LittleEndian.Uint16(resp[6:8]))
	assert.Equal(t, byte(steamcontroller.LizardModeOn), resp[9])
	assert.Equal(t, "Wired Controller", string(resp[16:16+len("Wired Controller")]))

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureGetSettingsValues})
	if !assert.True(t, handled) {
		return
	}

	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.FeatureGetSettingsValues), resp[0])
	assert.Contains(t, resp[2:2+int(resp[1])], byte(steamcontroller.SettingWirelessPacketVer))
	assert.Contains(t, resp[2:2+int(resp[1])], byte(steamcontroller.SettingIMUMode))
	assert.Contains(t, resp[2:2+int(resp[1])], byte(steamcontroller.SettingLizardMode))
	settings := resp[2 : 2+int(resp[1])]
	var foundIMUMode bool
	var foundWirelessPacketVer bool
	var foundRightTrackpadMode bool
	for index := 0; index+2 < len(settings); index += 3 {
		switch settings[index] {
		case steamcontroller.SettingIMUMode:
			foundIMUMode = true
			assert.Equal(t, uint16(steamcontroller.GyroModeOff), binary.LittleEndian.Uint16(settings[index+1:index+3]))
		case steamcontroller.SettingWirelessPacketVer:
			foundWirelessPacketVer = true
			assert.Equal(t, uint16(0), binary.LittleEndian.Uint16(settings[index+1:index+3]))
		case steamcontroller.SettingRightTrackpadMode:
			foundRightTrackpadMode = true
			assert.Equal(t, uint16(steamcontroller.TrackpadModeAbsoluteMouse), binary.LittleEndian.Uint16(settings[index+1:index+3]))
		}
	}
	assert.True(t, foundIMUMode)
	assert.True(t, foundWirelessPacketVer)
	assert.True(t, foundRightTrackpadMode)

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 6, []byte{0x00, steamcontroller.FeatureSetSettingsValues, 0x03, steamcontroller.SettingLizardMode, byte(steamcontroller.LizardModeOff), 0x00})
	if !assert.True(t, handled) {
		return
	}
	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureGetDeviceInfo})
	if !assert.True(t, handled) {
		return
	}
	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.LizardModeOff), resp[9])

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 11, []byte{0x00, steamcontroller.FeatureSetSettingsValues, 0x06, steamcontroller.SettingWirelessPacketVer, 0x02, 0x00, steamcontroller.SettingRightTrackpadMode, steamcontroller.TrackpadModeNone, 0x00})
	if !assert.True(t, handled) {
		return
	}
	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureClearDigitalMappings})
	if !assert.True(t, handled) {
		return
	}
	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 3, []byte{0x00, steamcontroller.FeatureLoadDefaultSettings, 0x00})
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, dev.HandleTransfer(gateTestCtx(), steamControllerKeyboardEndpoint, usbip.DirIn, nil))
	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 4, []byte{0x00, steamcontroller.FeatureGetDigitalMappings, 0x01, 0x00})
	if !assert.True(t, handled) {
		return
	}
	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.FeatureGetDigitalMappings), resp[0])
	assert.Equal(t, byte(0x01), resp[1])
	assert.Equal(t, byte(0xff), resp[2])
	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureGetSettingsValues})
	if !assert.True(t, handled) {
		return
	}
	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	settings = resp[2 : 2+int(resp[1])]
	foundWirelessPacketVer = false
	foundRightTrackpadMode = false
	for index := 0; index+2 < len(settings); index += 3 {
		switch settings[index] {
		case steamcontroller.SettingWirelessPacketVer:
			foundWirelessPacketVer = true
			assert.Equal(t, uint16(0), binary.LittleEndian.Uint16(settings[index+1:index+3]))
		case steamcontroller.SettingRightTrackpadMode:
			foundRightTrackpadMode = true
			assert.Equal(t, uint16(steamcontroller.TrackpadModeAbsoluteMouse), binary.LittleEndian.Uint16(settings[index+1:index+3]))
		}
	}
	assert.True(t, foundWirelessPacketVer)
	assert.True(t, foundRightTrackpadMode)

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 2, []byte{0x00, steamcontroller.FeatureGetDeviceInfo})
	if !assert.True(t, handled) {
		return
	}
	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.LizardModeOff), resp[9])

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, steamControllerInterface, 4, []byte{0x00, steamcontroller.FeatureGetStringAttribute, 0x15, steamcontroller.StringAttributeUnitSerial})
	if !assert.True(t, handled) {
		return
	}

	resp, handled = dev.HandleControl(0xa1, 0x01, 0x0300, steamControllerInterface, steamcontroller.InputReportLen, nil)
	if !assert.True(t, handled) {
		return
	}
	assert.Equal(t, byte(steamcontroller.FeatureGetStringAttribute), resp[0])
	assert.Equal(t, byte(len("SteamController-0001")), resp[1])
	assert.Equal(t, byte(steamcontroller.StringAttributeUnitSerial), resp[2])
	assert.Equal(t, "SteamController-0001", string(resp[3:3+int(resp[1])]))

	_, handled = dev.HandleControl(0x21, 0x09, 0x0300, 0, 2, []byte{0x00, steamcontroller.FeatureGetSettingsValues})
	assert.False(t, handled)
}

func TestFeedbackCommands(t *testing.T) {
	dev, err := steamcontroller.New(nil)
	if !assert.NoError(t, err) {
		return
	}

	var got steamcontroller.OutputState
	dev.SetOutputCallback(func(out steamcontroller.OutputState) {
		got = out
	})

	cmd := make([]byte, steamcontroller.InputReportLen)
	cmd[0] = steamcontroller.FeatureTriggerRumbleCommand
	cmd[1] = 9
	cmd[3] = 7
	cmd[4] = steamcontroller.IntensityMedium
	binary.LittleEndian.PutUint16(cmd[5:7], 500)
	binary.LittleEndian.PutUint16(cmd[7:9], 900)

	_, handled := dev.HandleControl(0x21, 0x09, uint16(0x0300), steamControllerInterface, uint16(len(cmd)), cmd)
	if !assert.True(t, handled) {
		return
	}
	rumble, ok := got.AsRumble()
	if !assert.True(t, ok) {
		return
	}
	assert.Equal(t, uint16(500), rumble.LeftSpeed)
	assert.Equal(t, uint16(900), rumble.RightSpeed)
}

func TestOutputCallbackReceivesNonHapticCommands(t *testing.T) {
	dev, err := steamcontroller.New(nil)
	if !assert.NoError(t, err) {
		return
	}

	var got steamcontroller.OutputState
	dev.SetOutputCallback(func(out steamcontroller.OutputState) {
		got = out
	})

	cmd := []byte{0x00, steamcontroller.FeatureClearDigitalMappings}
	_, handled := dev.HandleControl(0x21, 0x09, uint16(0x0300), steamControllerInterface, uint16(len(cmd)), cmd)
	if !assert.True(t, handled) {
		return
	}

	assert.Equal(t, byte(steamcontroller.FeatureClearDigitalMappings), got.CommandID())
}

func TestOutputParsers(t *testing.T) {
	var out steamcontroller.OutputState

	out.Data[0] = steamcontroller.FeatureTriggerHapticPulse
	binary.LittleEndian.PutUint16(out.Data[3:5], 0x0190)
	binary.LittleEndian.PutUint16(out.Data[5:7], 0x0000)
	binary.LittleEndian.PutUint16(out.Data[7:9], 0x0001)
	out.Data[9] = 0x02
	pulse, ok := out.AsHapticPulse()
	if !assert.True(t, ok) {
		return
	}
	assert.Equal(t, uint16(0x0190), pulse.Duration)
	assert.Equal(t, uint16(0x0001), pulse.Count)
	assert.Equal(t, byte(0x02), pulse.Gain)

	out = steamcontroller.OutputState{}
	out.Data[0] = steamcontroller.FeaturePlayAudio
	out.Data[1] = 4
	copy(out.Data[2:6], []byte{0x11, 0x22, 0x33, 0x44})
	audio, ok := out.AsPlayAudio()
	if !assert.True(t, ok) {
		return
	}
	assert.Equal(t, []byte{0x11, 0x22, 0x33, 0x44}, audio.Payload)
}

func TestAPIStreamAndUSBInput(t *testing.T) {
	s := viiperTesting.NewTestServer(t)
	defer s.UsbServer.Close()
	defer s.ApiServer.Close()

	r := s.ApiServer.Router()
	r.Register("bus/{id}/add", handler.BusDeviceAdd(s.UsbServer, s.ApiServer))
	r.RegisterStream("bus/{busId}/{deviceid}", api.DeviceStreamHandler(s.UsbServer))

	if err := s.ApiServer.Start(); err != nil {
		t.Fatalf("Failed to start API server: %v", err)
	}

	b, err := virtualbus.NewWithBusID(1)
	if err != nil {
		t.Fatalf("Failed to create virtual bus: %v", err)
	}
	defer b.Close()
	_ = s.UsbServer.AddBus(b)

	client := viiperclient.New(s.ApiServer.Addr())
	stream, _, err := client.AddDeviceAndConnect(context.Background(), b.BusID(), "steamcontroller", nil)
	if !assert.NoError(t, err) {
		return
	}
	defer stream.Close()

	usbipClient := viiperTesting.NewUsbIpClient(t, s.UsbServer.Addr())
	devs, err := usbipClient.ListDevices()
	if !assert.NoError(t, err) || !assert.Len(t, devs, 1) {
		return
	}
	imp, err := usbipClient.AttachDevice(devs[0].BusID)
	if !assert.NoError(t, err) {
		return
	}
	if imp != nil && imp.Conn != nil {
		defer imp.Conn.Close()
	}

	seq := uint32(0)
	readInputReport := func(timeout time.Duration) ([]byte, error) {
		seq++
		cmd := usbip.CmdSubmit{
			Basic:             usbip.HeaderBasic{Command: usbip.CmdSubmitCode, Seqnum: seq, Devid: 0, Dir: usbip.DirIn, Ep: steamControllerEndpoint},
			TransferBufferLen: 255,
		}
		_ = imp.Conn.SetDeadline(time.Now().Add(timeout))
		if err := cmd.Write(imp.Conn); err != nil {
			return nil, err
		}
		var retHdr [48]byte
		if err := usbip.ReadExactly(imp.Conn, retHdr[:]); err != nil {
			return nil, err
		}
		actual := binary.BigEndian.Uint32(retHdr[24:28])
		data := make([]byte, int(actual))
		if actual > 0 {
			if err := usbip.ReadExactly(imp.Conn, data); err != nil {
				return nil, err
			}
		}
		_ = imp.Conn.SetDeadline(time.Time{})
		return data, nil
	}

	state := steamcontroller.InputState{A: true, LPadTouch: true, LPadX: -1234, LPadY: 4321, LTrigger: 2048}
	data, err := state.MarshalBinary()
	if !assert.NoError(t, err) {
		return
	}
	if _, err := stream.Write(data); !assert.NoError(t, err) {
		return
	}
	got, err := readInputReport(750 * time.Millisecond)
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, got, steamcontroller.InputReportLen)
	assert.Equal(t, byte(0x80), got[8]&0x80)
	assert.Equal(t, byte(0x08), got[10]&0x08)
	assert.Equal(t, uint16(0xfb2e), binary.LittleEndian.Uint16(got[16:18]))

	cmd := make([]byte, steamcontroller.InputReportLen)
	cmd[0] = steamcontroller.FeatureTriggerHapticCommand
	cmd[1] = 13
	cmd[2] = steamcontroller.PadSideLeft
	cmd[3] = steamcontroller.CommandTypeClick
	cmd[4] = steamcontroller.IntensityLong
	cmd[5] = 0xf9
	dev := b.GetAllDeviceMetas()[0].Dev
	dev.HandleTransfer(gateTestCtx(), steamControllerEndpoint, usbip.DirOut, cmd)

	var feedback [steamcontroller.InputReportLen]byte
	_ = stream.SetReadDeadline(time.Now().Add(750 * time.Millisecond))
	_, err = io.ReadFull(stream, feedback[:])
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, byte(steamcontroller.FeatureTriggerHapticCommand), feedback[0])
	assert.Equal(t, byte(steamcontroller.CommandTypeClick), feedback[3])
}

// gateTestCtx returns a context with a short deadline so gated interrupt-IN
// HandleTransfer calls in tests never block: a signalled input gate completes
// immediately (GateFresh), an unsignalled one completes at the deadline
// (GateDeadline) — both build a report from current state. (Port of upstream
// data-driven completion, 7e33d2d3.)
func gateTestCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_ = cancel
	return ctx
}
