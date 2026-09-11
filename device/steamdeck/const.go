package steamdeck

const (
	DefaultVID = 0x28de
	DefaultPID = 0x1205
)

const (
	DefaultProfile = "steamdeck"
	InputReportID  = 0x09
	InputReportLen = 64
	// SDL3's HIDAPI Steam Deck driver hard-equality checks
	// header.ucLength == 64 at report ingress
	// (SDL/src/joystick/hidapi/SDL_hidapi_steamdeck.c
	// HIDAPI_DriverSteamDeck_UpdateDevice). Writing 56 (the payload-only
	// length used by the pre-Deck Steam Controller) caused SDL3 to silently
	// drop every frame, so sticks/gyro/accel all read as zero downstream
	// (verified live 2026-05-31 against SdlGyroTester with a virtual
	// Steam Deck target — sensors=True at HasSensor, GetSensorData=0,0,0).
	// Match the full report length here.
	DeckInputPayloadLen = 64
)

const (
	FeatureSetDigitalMappings   = 0x80
	FeatureClearDigitalMappings = 0x81
	FeatureGetDigitalMappings   = 0x82
	FeatureGetAttributesValues  = 0x83
	FeatureGetAttributeLabel    = 0x84
	FeatureSetDefaultMappings   = 0x85
	FeatureFactoryReset         = 0x86
	FeatureSetSettingsValues    = 0x87
	FeatureClearSettingsValues  = 0x88
	FeatureGetSettingsValues    = 0x89
	FeatureGetSettingLabel      = 0x8a
	FeatureGetSettingsMaxs      = 0x8b
	FeatureGetSettingsDefaults  = 0x8c
	FeatureSetControllerMode    = 0x8d
	FeatureLoadDefaultSettings  = 0x8e
	FeatureTriggerHapticPulse   = 0x8f
	FeatureTurnOffController    = 0x9f
	FeatureGetDeviceInfo        = 0xa1
	FeatureGetStringAttribute   = 0xae
	FeatureRadioEraseRecords    = 0xaf
	FeatureRadioWriteRecord     = 0xb0
	FeatureSetDongleSetting     = 0xb1
	FeatureDongleDisconnect     = 0xb2
	FeatureDongleCommitDevice   = 0xb3
	FeatureGetWirelessState     = 0xb4
	FeatureCalibrateGyro        = 0xb5
	FeaturePlayAudio            = 0xb6
	FeatureAudioUpdateStart     = 0xb7
	FeatureAudioUpdateData      = 0xb8
	FeatureAudioUpdateComplete  = 0xb9
	FeatureGetChipID            = 0xba
	FeatureResetIMU             = 0xce
	FeatureTriggerHapticCommand = 0xea
	FeatureTriggerRumbleCommand = 0xeb
)

const (
	AttributeUniqueID             = 0x00
	AttributeProductID            = 0x01
	AttributeCapabilities         = 0x02
	AttributeFirmwareBuildTime    = 0x04
	AttributeBoardRevision        = 0x09
	AttributeBootloaderBuildTime  = 0x0a
	AttributeConnectionIntervalUs = 0x0b
)

const (
	StringAttributeBoardSerial = 0x00
	StringAttributeUnitSerial  = 0x01
)

const (
	SettingLeftTrackpadMode           = 0x07
	SettingRightTrackpadMode          = 0x08
	SettingLizardMode                 = 0x09
	SettingSmoothAbsoluteMouse        = 0x18
	SettingIMUMode                    = 0x30
	SettingLeftTrackpadClickPressure  = 0x34
	SettingRightTrackpadClickPressure = 0x35
	SettingSteamWatchdogEnable        = 0x47
)

const (
	GyroModeOff             = 0x00
	GyroModeSteering        = 0x01
	GyroModeTilt            = 0x02
	GyroModeSendOrientation = 0x04
	GyroModeSendRawAccel    = 0x08
	GyroModeSendRawGyro     = 0x10
)

const (
	TrackpadModeNone = 0x07
	LizardModeOff    = 0x00
	LizardModeOn     = 0x01
)

const (
	CapabilityGamepad = 0x00000004
)

const (
	buttonByte8A  = 0x80
	buttonByte8X  = 0x40
	buttonByte8B  = 0x20
	buttonByte8Y  = 0x10
	buttonByte8L1 = 0x08
	buttonByte8R1 = 0x04
	buttonByte8L2 = 0x02
	buttonByte8R2 = 0x01

	buttonByte9L5      = 0x80
	buttonByte9Menu    = 0x40
	buttonByte9Steam   = 0x20
	buttonByte9Options = 0x10
	buttonByte9Down    = 0x08
	buttonByte9Left    = 0x04
	buttonByte9Right   = 0x02
	buttonByte9Up      = 0x01

	buttonByte10L3        = 0x40
	buttonByte10RPadTouch = 0x10
	buttonByte10LPadTouch = 0x08
	buttonByte10RPadPress = 0x04
	buttonByte10LPadPress = 0x02
	buttonByte10R5        = 0x01

	buttonByte11R3 = 0x04

	buttonByte13RStickTouch = 0x80
	buttonByte13LStickTouch = 0x40
	buttonByte13R4          = 0x04
	buttonByte13L4          = 0x02

	buttonByte14QuickAccess = 0x04
)

const (
	CommandTypeOff   = 0
	CommandTypeTick  = 1
	CommandTypeClick = 2
)

const (
	PadSideLeft  = 0
	PadSideRight = 1
	PadSideBoth  = 2
)

const (
	IntensityDefault = 0
	IntensityShort   = 1
	IntensityMedium  = 2
	IntensityLong    = 3
	IntensityInsane  = 4
)
