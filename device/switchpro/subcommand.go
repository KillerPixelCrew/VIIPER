package switchpro

import (
	"encoding/binary"
	"log/slog"
)

// handleUSBCommand processes USB vendor command reports (0x80).
// These are part of the Pro Controller USB initialization handshake.
func (d *SwitchPro) handleUSBCommand(data []byte) {
	if len(data) < 2 {
		return
	}

	cmd := data[1]
	reply := make([]byte, InputReportSize)
	reply[0] = ReportIDUSBReply // 0x81

	switch cmd {
	case USBCmdRequestMAC: // 0x01 — request MAC address
		reply[1] = USBCmdRequestMAC
		reply[2] = 0x00 // padding
		// Device type: 0x01=Joy-Con(L), 0x02=Joy-Con(R), 0x03=Pro Controller
		switch d.profile {
		case ProfileJoyConLeft:
			reply[3] = 0x01
		case ProfileJoyConRight:
			reply[3] = 0x02
		default:
			reply[3] = 0x03
		}
		mac := fakeMACForProfile(d.profile)
		copy(reply[4:10], mac[:])
		slog.Info("SwitchPro: USB handshake - MAC request", "profile", d.profile)

	case USBCmdHandshake: // 0x02 — handshake
		reply[1] = USBCmdHandshake
		slog.Info("SwitchPro: USB handshake - handshake ACK")

	case USBCmdSetBaudrate: // 0x03 — set baudrate
		reply[1] = USBCmdSetBaudrate
		slog.Info("SwitchPro: USB handshake - baudrate set")

	case USBCmdForceHID: // 0x04 — force USB HID mode
		reply[1] = USBCmdForceHID
		d.usbReady = true
		slog.Info("SwitchPro: USB handshake - HID mode active, starting input reports")

	default:
		slog.Warn("SwitchPro: unknown USB command", "cmd", cmd)
		reply[1] = cmd
	}

	d.pendingReplyMu.Lock()
	d.pendingReply = reply
	d.pendingReplyMu.Unlock()
	d.gate.Signal()
}

// handleSubcommand processes subcommand output reports (0x01).
// Format: [0x01, packetNum, rumble(8 bytes), subcmdID, args...]
func (d *SwitchPro) handleSubcommand(data []byte) {
	if len(data) < 11 {
		return
	}

	// Receiving a subcommand means the host is communicating.
	// Mark ready if not already (some drivers skip the 0x80 handshake).
	if !d.usbReady {
		d.usbReady = true
		slog.Info("SwitchPro: marking ready via subcommand (no 0x80 handshake)")
	}

	// Parse and forward rumble data (bytes 2-9).
	if d.vibEnabled {
		left, right := parseRumble(data[2:10])
		if d.outputFunc != nil {
			d.outputFunc(OutputState{RumbleLeft: left, RumbleRight: right})
		}
	}

	subcmd := data[10]
	var replyData []byte

	switch subcmd {
	case SubcmdDeviceInfo:
		replyData = d.replyDeviceInfo()
	case SubcmdSetInputMode:
		d.replySetInputMode(data)
	case SubcmdTriggerElapsed:
		replyData = d.replyTriggerElapsed()
	case SubcmdSetShipment:
		replyData = nil // ACK only
	case SubcmdSPIFlashRead:
		replyData = d.replySPIFlashRead(data)
	case SubcmdSetPlayerLights:
		d.replySetPlayerLights(data)
	case SubcmdSetHomeLED:
		replyData = nil // ACK only
	case SubcmdEnableIMU:
		d.replyEnableIMU(data)
	case SubcmdEnableVibration:
		d.replyEnableVibration(data)
	default:
		slog.Info("SwitchPro: unknown subcommand", "subcmd", subcmd)
		replyData = nil
	}

	reply := d.buildSubcmdReply(subcmd, replyData)

	d.pendingReplyMu.Lock()
	d.pendingReply = reply
	d.pendingReplyMu.Unlock()
	d.gate.Signal()
}

// buildSubcmdReply builds a 0x21 subcommand reply report (64 bytes).
// Format: [0x21, timer, battery, buttons(3), sticks(6), vibrator, ACK|subcmd, replyData...]
func (d *SwitchPro) buildSubcmdReply(subcmd uint8, replyData []byte) []byte {
	b := make([]byte, InputReportSize)
	b[0] = ReportIDSubcmdReply
	d.timer++
	b[1] = d.timer
	b[2] = BatteryFull

	// Abbreviated current input state (bytes 3-12).
	d.stateMu.Lock()
	var st InputState
	if d.inputState != nil {
		st = *d.inputState
	}
	d.stateMu.Unlock()

	b[3] = byte(st.Buttons & 0xFF)
	b[4] = byte((st.Buttons >> 8) & 0xFF)
	b[5] = byte((st.Buttons >> 16) & 0xFF)

	ls := packStick12(st.LX, st.LY)
	b[6] = ls[0]
	b[7] = ls[1]
	b[8] = ls[2]

	rs := packStick12(st.RX, st.RY)
	b[9] = rs[0]
	b[10] = rs[1]
	b[11] = rs[2]

	b[12] = 0x00 // vibrator

	// Subcommand ACK (byte 13): ACK flag | subcmd
	b[13] = 0x80 | subcmd
	// Subcommand ID echo (byte 14)
	b[14] = subcmd

	// Reply data starts at byte 15.
	if replyData != nil {
		n := len(replyData)
		if n > 49 { // max 64 - 15 = 49 bytes
			n = 49
		}
		copy(b[15:15+n], replyData[:n])
	}

	return b
}

// replyDeviceInfo returns device info for subcommand 0x02.
func (d *SwitchPro) replyDeviceInfo() []byte {
	reply := make([]byte, 12)
	// Firmware version
	reply[0] = 0x04 // major
	reply[1] = 0x33 // minor

	// Device type
	switch d.profile {
	case ProfileJoyConLeft:
		reply[2] = 0x01 // Joy-Con (L)
	case ProfileJoyConRight:
		reply[2] = 0x02 // Joy-Con (R)
	default:
		reply[2] = 0x03 // Pro Controller
	}

	reply[3] = 0x02 // Unknown (always 0x02)

	// MAC address (6 bytes)
	mac := fakeMACForProfile(d.profile)
	copy(reply[4:10], mac[:])

	reply[10] = 0x03 // Unknown (always 0x03)
	reply[11] = 0x02 // Use colors from SPI (0x02 = yes)

	slog.Info("SwitchPro: subcommand - device info")
	return reply
}

// replySetInputMode handles subcommand 0x03 (set input report mode).
func (d *SwitchPro) replySetInputMode(data []byte) {
	if len(data) >= 12 {
		mode := data[11]
		slog.Info("SwitchPro: subcommand - set input mode", "mode", mode)
	}
	// Always accept, we always send 0x30 full reports.
}

// replyTriggerElapsed handles subcommand 0x04.
func (d *SwitchPro) replyTriggerElapsed() []byte {
	// Return zeroed trigger elapsed time.
	return make([]byte, 7)
}

// replySPIFlashRead handles subcommand 0x10 (SPI flash read).
// Returns appropriate calibration data for known addresses, zeros otherwise.
func (d *SwitchPro) replySPIFlashRead(data []byte) []byte {
	if len(data) < 16 {
		return nil
	}

	// Arguments: data[11..14] = address (uint32 LE), data[15] = length
	addr := binary.LittleEndian.Uint32(data[11:15])
	length := data[15]
	if length > 29 { // max reply data
		length = 29
	}

	reply := make([]byte, 5+int(length))
	binary.LittleEndian.PutUint32(reply[0:4], addr)
	reply[4] = length

	// Fill with appropriate data based on address.
	// SPI flash memory map per dekuNukem/Nintendo_Switch_Reverse_Engineering:
	//   0x6020-0x6037: Factory 6-axis IMU calibration (24 bytes)
	//   0x603D-0x604E: Factory analog stick calibration (18 bytes)
	//   0x6050-0x6055: Body + button colors (6 bytes)
	//   0x8010-0x8025: User analog stick calibration (22 bytes)
	//   0x8026-0x8027: User 6-axis IMU calibration presence (magic 0xB2A1 = present)
	//   0x8028-0x803F: User 6-axis IMU calibration (24 bytes)
	flashData := reply[5:]
	switch {
	case addr == 0x6020:
		// Factory 6-axis IMU calibration (24 bytes).
		// Format: 6x int16 offsets + 6x int16 scale factors.
		imuCal := defaultIMUCalibration()
		n := int(length)
		if n > 24 {
			n = 24
		}
		copy(flashData, imuCal[:n])

	case addr == 0x6050 && length >= 6:
		// Body color (3 bytes) + button color (3 bytes)
		// Pro Controller default: gray body, gray buttons
		flashData[0] = 0x32 // body R
		flashData[1] = 0x32 // body G
		flashData[2] = 0x32 // body B
		flashData[3] = 0xFF // button R
		flashData[4] = 0xFF // button G
		flashData[5] = 0xFF // button B

	case addr == 0x603D:
		// Factory analog stick calibration (left + right, 18 bytes total)
		cal := defaultStickCalibration()
		n := int(length)
		if n > 18 {
			n = 18
		}
		copy(flashData, cal[:n])

	case addr == 0x6080 && length >= 6:
		// Factory accelerometer horizontal offsets (6 bytes = 3x int16 LE).
		// Compensates for controller body geometry when resting flat.
		// Pro Controller defaults per dekuNukem: X=-688, Y=0, Z=4038
		off := defaultAccelHorizontalOffset(d.profile)
		n := int(length)
		if n > 6 {
			n = 6
		}
		copy(flashData, off[:n])

	case addr == 0x8010 && length >= 22:
		// User analog stick calibration — leave as zeros (no user cal = use factory)

	case addr == 0x8026 && length >= 2:
		// User 6-axis IMU calibration presence check.
		// Leave as zeros = no user calibration → host uses factory data at 0x6020.
	}

	slog.Info("SwitchPro: subcommand - SPI read", "addr", addr, "len", length)
	return reply
}

// replySetPlayerLights handles subcommand 0x30.
func (d *SwitchPro) replySetPlayerLights(data []byte) {
	if len(data) >= 12 {
		d.playerLights = data[11]
		slog.Info("SwitchPro: subcommand - set player lights", "pattern", d.playerLights)
	}
}

// replyEnableIMU handles subcommand 0x40.
func (d *SwitchPro) replyEnableIMU(data []byte) {
	if len(data) >= 12 {
		d.imuEnabled = data[11] != 0
		slog.Info("SwitchPro: subcommand - enable IMU", "enabled", d.imuEnabled)
	}
}

// replyEnableVibration handles subcommand 0x48.
func (d *SwitchPro) replyEnableVibration(data []byte) {
	if len(data) >= 12 {
		d.vibEnabled = data[11] != 0
		slog.Info("SwitchPro: subcommand - enable vibration", "enabled", d.vibEnabled)
	}
}
