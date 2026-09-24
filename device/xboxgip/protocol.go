package xboxgip

import "encoding/binary"

// gipMetadata is the compiled MS-GIPUSB metadata blob announcing this
// device as an Xbox Elite Series 2 controller (firmware 5.21) with the
// minimum interface-GUID set needed for paddle support per Linux
// xbox_gip.c, and nothing extra that triggers Windows "requires further
// installation" by demanding child-interface drivers.
//
// CRITICAL GUID for paddles: IEliteButtons 37D19FF7-B5C6-49D1-A75E-03B24BEF8C89.
// This is the ONLY GUID the Linux kernel checks for paddle handling.
// Earlier attempts added IConsoleFunctionMap + IProgrammableGamepad on
// research recommendations; both caused Windows to flag the device
// "needs additional install" because dc1-controller.inf expects child
// INFs for those interfaces that don't exist. The kernel patch was
// explicit that they don't actually gate paddles.
//
// Built at package init from buildMetadataBlob() so future changes are
// just GUID-list edits, no manual byte-offset arithmetic.
var gipMetadata = buildMetadataBlob()

// Standard interface GUIDs declared by every Xbox GIP gamepad.
//
// guidVirtualDevice (DFD26825-110A-4E94-B937-B27CE47B2540) is verified
// present in xboxgip.sys at offset 0x4cce0, in the contiguous GUID
// table 0x4ccb0-0x4cde0 alongside IGamepad, IEliteButtons,
// IExtendedDeviceFlags, IControllerProfileModeState, etc. The Linux
// xbox_gip.c patch documents it as "IVirtualDevice — declared but
// unused by the driver"; xboxgipsynthetic.inf separately sets
// SyntheticDeviceFlags=1 to flip the device onto a code path that
// skips USB-handshake requirements. Declaring this GUID in the
// metadata is our best in-protocol shot at telling xboxgip.sys "this
// is a virtual device — don't wait for child PDOs that USBIP can't
// produce."
var (
	guidController           = [16]byte{0x56, 0xFF, 0x76, 0x97, 0xFD, 0x9B, 0x81, 0x45, 0xAD, 0x45, 0xB6, 0x45, 0xBB, 0xA5, 0x26, 0xD6}
	guidGamepad              = [16]byte{0x2C, 0x40, 0x2E, 0x08, 0xDF, 0x07, 0xE1, 0x45, 0xA5, 0xAB, 0xA3, 0x12, 0x7A, 0xF1, 0x97, 0xB5}
	guidNavigationController = [16]byte{0xE7, 0x1F, 0xF3, 0xB8, 0x86, 0x73, 0xE9, 0x40, 0xA9, 0xF8, 0x2F, 0x21, 0x26, 0x3A, 0xCF, 0xB7}
	guidDevAuthPCOptOut      = [16]byte{0x77, 0xCE, 0x34, 0x7A, 0xE2, 0x7D, 0xC6, 0x45, 0x8C, 0xA4, 0x00, 0x42, 0xC0, 0x8B, 0xD9, 0x4A}
	guidEliteButtons         = [16]byte{0xF7, 0x9F, 0xD1, 0x37, 0xC6, 0xB5, 0xD1, 0x49, 0xA7, 0x5E, 0x03, 0xB2, 0x4B, 0xEF, 0x8C, 0x89}
	//nolint:unused // protocol reference for the unfinished GIP work
	guidVirtualDevice = [16]byte{0x25, 0x68, 0xD2, 0xDF, 0x0A, 0x11, 0x94, 0x4E, 0xB9, 0x37, 0xB2, 0x7C, 0xE4, 0x7B, 0x25, 0x40}
)

// buildMetadataBlob constructs the GIP metadata blob byte-for-byte
// matching xone's gip_parse_info_element layout. See [MS-GIPUSB] §2.2.2.
func buildMetadataBlob() []byte {
	guids := [][16]byte{
		guidController,
		guidGamepad,
		guidNavigationController,
		guidDevAuthPCOptOut,
		guidEliteButtons,
		// Test D (dropping guidEliteButtons + changing fw to 2.5) BROKE the
		// bridge fast-path match for PID 0x02D1 — host no longer fired
		// Quiesce, just 3 metadata retries then STOP. Reverted to Test C
		// state (5.21 fw + EliteButtons): that reliably reaches Quiesce +
		// Active + first input report, then STOPs at cycle 3 post-Active.
		// Cycle-3 post-Active STOP is a different problem from bridge
		// matching and needs a different angle.
		// IVirtualDevice intentionally OMITTED for USB transport. Per Ghidra
		// disassembly of xboxgip.sys FUN_140024720 (the metadata-arrival
		// handler), the post-parse predicate is:
		//   parsedMetadata.IVirtualDeviceDeclared == (device.transport == 2)
		// device.transport == 2 means "synthetic" (SWD\XBOXGIP_SYNTHETIC,
		// Bluetooth, or other non-USB). USB-attached devices have transport
		// != 2. If we declare IVirtualDevice while transport is USB the
		// handler returns STATUS_DEVICE_FEATURE_NOT_SUPPORTED (0xC000A013),
		// the metadata is discarded, and no IGamepad device-interface
		// instance is ever published. Result: device shows Status=OK and
		// xboxgip.sys exchanges LED/rumble, but Windows.Gaming.Input never
		// enumerates the controller.
	}
	capsOut := []byte{0x01, 0x02, 0x03, 0x04, 0x06, 0x07}
	// 0x07 (Guide / VirtualKey) and 0x0C (Firmware) included so the host
	// accepts our Guide-button packets and firmware-version announces.
	capsIn := []byte{0x01, 0x04, 0x05, 0x06, 0x07, 0x0C, 0x0A}
	className := "Windows.Xbox.Input.Gamepad"
	type fwVer struct{ major, minor uint16 }
	// Firmware 5.21 — matches the Elite 2 paddle-firmware revision the
	// Linux kernel's XBE2_5 layout expects. Production default.
	//
	// 2026-05-19: this value doesn't really matter — xboxgip target was
	// pulled from the widget after RE confirmed no USB-IP virtual gamepad
	// can publish an IGamepad PDO through xboxgip.sys (see
	// [[project_gip_definitive_walls_2026-05-19]]). Kept the appendGIPLen2
	// encoding fix in fragmentMetadata + buildMetadataComplete since that
	// IS a real protocol bug that affected all GIP traffic.
	firmware := []fwVer{{5, 21}}
	type message struct {
		marker, cmd, length, opt2, options byte
	}
	// msg[0] = 0x20 input report, payload length 0x2A (42 bytes —
	//          covers paddle byte at payload offset 14 and profile-slot
	//          byte at payload offset 20). options=0x10 = upstream.
	// msg[1] = 0x09 rumble, 9 bytes downstream (options=0x08).
	msgs := []message{
		{marker: 0x17, cmd: 0x20, length: 0x2A, opt2: 0x01, options: 0x10},
		{marker: 0x17, cmd: 0x09, length: 0x09, opt2: 0x01, options: 0x08},
	}

	// Section sizes
	firmwareLen := 1 + 4*len(firmware)
	audioLen := 1 // count=0, no entries
	capsOutLen := 1 + len(capsOut)
	capsInLen := 1 + len(capsIn)
	classesLen := 1 + 2 + len(className)
	interfacesLen := 1 + 16*len(guids)
	messagesLen := 1 + 23*len(msgs)

	// Section absolute byte offsets (after 16-byte header + 16-byte
	// offsets table + 6-byte reserved-padding region from the spec).
	headerSize := 16
	offsetsSize := 16
	padSize := 6
	dataStart := headerSize + offsetsSize + padSize
	firmwareAt := dataStart
	audioAt := firmwareAt + firmwareLen
	capsOutAt := audioAt + audioLen
	capsInAt := capsOutAt + capsOutLen
	classesAt := capsInAt + capsInLen
	interfacesAt := classesAt + classesLen
	messagesAt := interfacesAt + interfacesLen
	total := messagesAt + messagesLen

	b := make([]byte, total)

	// Header: header_size + version(1.0) + reserved + data_len.
	b[0] = byte(headerSize)
	b[1] = byte(headerSize >> 8)
	b[2] = 0x01 // version major = 1
	b[14] = byte(total)
	b[15] = byte(total >> 8)

	// Offsets table: relative to byte 16 (post-header).
	off16 := func(at int) uint16 { return uint16(at - headerSize) }
	put16 := func(idx int, v uint16) {
		b[idx] = byte(v)
		b[idx+1] = byte(v >> 8)
	}
	put16(16, off16(messagesAt))   // client_commands
	put16(18, off16(firmwareAt))   // firmware_versions
	put16(20, off16(audioAt))      // audio_formats
	put16(22, off16(capsOutAt))    // capabilities_out
	put16(24, off16(capsInAt))     // capabilities_in
	put16(26, off16(classesAt))    // classes (preferred types)
	put16(28, off16(interfacesAt)) // interfaces
	put16(30, 0)                   // hid_descriptor — none

	// Firmware versions section.
	b[firmwareAt] = byte(len(firmware))
	for i, fv := range firmware {
		off := firmwareAt + 1 + 4*i
		b[off] = byte(fv.major)
		b[off+1] = byte(fv.major >> 8)
		b[off+2] = byte(fv.minor)
		b[off+3] = byte(fv.minor >> 8)
	}

	// Audio formats — count = 0.
	b[audioAt] = 0

	// Capabilities_out + capabilities_in (counts + 1-byte entries each).
	b[capsOutAt] = byte(len(capsOut))
	copy(b[capsOutAt+1:], capsOut)
	b[capsInAt] = byte(len(capsIn))
	copy(b[capsInAt+1:], capsIn)

	// Classes (preferred types) — count(1) + u16 length + utf-8 bytes.
	b[classesAt] = 1
	b[classesAt+1] = byte(len(className))
	b[classesAt+2] = byte(len(className) >> 8)
	copy(b[classesAt+3:], className)

	// Interfaces — count + N×16-byte GUIDs.
	b[interfacesAt] = byte(len(guids))
	for i, g := range guids {
		copy(b[interfacesAt+1+i*16:], g[:])
	}

	// Messages — count + N×23-byte descriptors. Each descriptor:
	//   marker(1) unknown1(1) cmd(1) length(1) unknown2[3] options(1) unknown3[15]
	b[messagesAt] = byte(len(msgs))
	for i, m := range msgs {
		off := messagesAt + 1 + 23*i
		b[off] = m.marker
		b[off+2] = m.cmd
		b[off+3] = m.length
		b[off+5] = m.opt2
		b[off+7] = m.options
	}

	return b
}

// Extended Compatible ID OS Descriptor (40 bytes).
// Returns "XGIP10" as the compatible ID, triggering xboxgip.sys driver loading.
var extCompatIDDescriptor = [40]byte{
	// Header (16 bytes)
	0x28, 0x00, 0x00, 0x00, // dwLength = 40
	0x00, 0x01, // bcdVersion = 1.00
	0x04, 0x00, // wIndex = 4 (Extended Compatible ID)
	0x01,                                     // bCount = 1 function
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // reserved
	// Function section (24 bytes)
	0x00,                                           // bFirstInterfaceNumber = 0
	0x02,                                           // bNumInterfaces = 2 (data + bulk expansion — audio dropped in Test A)
	0x58, 0x47, 0x49, 0x50, 0x31, 0x30, 0x00, 0x00, // compatibleID = "XGIP10\0\0"
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // subCompatibleID = "\0\0\0\0\0\0\0\0"
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // reserved
}

// Maximum GIP fragment payload (64-byte USB packet minus 6-byte fragment header).
const gipFragMaxPayload = 58

// buildHelloMessage builds the GIP Device Arrival announcement (command 0x02).
//
// Wire format per MS-GIPUSB §2.1.1.2 (28-byte payload):
//
//	offset 0..7   : Device ID (uint64 LE; numeric ID 0000FFFB56495052)
//	offset 8..9   : VID (uint16 LE)
//	offset 10..11 : PID (uint16 LE)
//	offset 12..13 : Firmware major (uint16 LE)
//	offset 14..15 : Firmware minor (uint16 LE)
//	offset 16..17 : Firmware build (uint16 LE)
//	offset 18..19 : Firmware revision (uint16 LE)
//	offset 20     : Hardware major (1 byte)
//	offset 21     : Hardware minor (1 byte)
//	offset 22..23 : RF protocol version (uint16 LE = 0x0001)
//	offset 24..25 : Security protocol version (uint16 LE = 0x0001)
//	offset 26..27 : GIP version (uint16 LE = 0x0001)
//
// 2026-05-16: prior version wrote HW as a uint16 instead of two bytes,
// shifting RF/Sec/GIP protocol-version fields by 1 byte. Windows would
// then see RF version = 0x0015 = 21 instead of 0x0001 and reject the
// Hello as an unsupported protocol revision — no metadata-request ever
// followed. FW also changed from 5.21 to 1.0 to match the Microsoft
// example metadata blob (the host requires Hello FW major/minor to match
// at least one fwVer entry in the device's metadata; we declare {1,0}).
func buildHelloMessage(seq uint8, vid, pid uint16) []byte {
	msg := make([]byte, 32)
	// GIP header (4 bytes total before payload)
	msg[0] = GIPAnnounce   // 0x02
	msg[1] = GIPFlagSystem // 0x20
	msg[2] = seq
	msg[3] = 28 // payload length 0x1c

	// Device ID (8 bytes): little-endian uint64 matching USB serial
	// "0000FFFB56495052". xboxgip.sys clears payload bytes 6..7 before
	// storing the slot ID, which only makes sense when those bytes are the
	// high 16 bits of the little-endian QWORD.
	binary.LittleEndian.PutUint64(msg[4:12], 0x0000FFFB56495052)

	// VID / PID.
	binary.LittleEndian.PutUint16(msg[12:14], vid)
	binary.LittleEndian.PutUint16(msg[14:16], pid)

	// Firmware 5.21 — Elite 2 firmware revision the kernel's XBE2_5 paddle
	// layout expects (per xbox_gip.c gip_enable_elite_buttons). Production
	// default. See [[project_gip_definitive_walls_2026-05-19]] for why
	// the xboxgip target ultimately can't publish a child PDO over USB-IP.
	binary.LittleEndian.PutUint16(msg[16:18], 5)  // major
	binary.LittleEndian.PutUint16(msg[18:20], 21) // minor
	binary.LittleEndian.PutUint16(msg[20:22], 0)  // build
	binary.LittleEndian.PutUint16(msg[22:24], 0)  // revision

	// Hardware version (single bytes, NOT uint16).
	msg[24] = 1 // HW major
	msg[25] = 0 // HW minor

	// Protocol versions — three uint16 LE values, all 0x0001.
	binary.LittleEndian.PutUint16(msg[26:28], 1) // RF protocol version
	binary.LittleEndian.PutUint16(msg[28:30], 1) // Security protocol version
	binary.LittleEndian.PutUint16(msg[30:32], 1) // GIP version

	return msg
}

// buildStatusMessage builds a GIP Device Status message (command 0x03).
// Reports wired power (no battery) and connected state.
func buildStatusMessage(seq uint8) []byte {
	return []byte{
		GIPStatus,     // 0x03
		GIPFlagSystem, // 0x20
		seq,
		0x04,             // payload length = 4
		0x00,             // battery: level=0 (wired), type=0 (none/wired)
		0x00, 0x00, 0x00, // reserved
	}
}

// buildFirmwareMessage builds a GIP Firmware (0x0C) message reporting our
// firmware version. Per Linux kernel xbox_gip.c gip_handle_command_firmware,
// the driver re-runs gip_enable_elite_buttons() on receipt of this message
// for PID 0x0B00 — which is what flips the device into Elite-2 paddle mode.
//
// Payload layout (14+ bytes):
//
//	byte 0:    subtype = 0x01 (firmware-version announcement)
//	bytes 1-5: reserved
//	bytes 6-7: major u16 LE
//	bytes 8-9: minor u16 LE
//	bytes 10-11: build u16 LE
//	bytes 12-13: revision u16 LE
//
//nolint:unused // protocol reference for the unfinished GIP work
func buildFirmwareMessage(seq uint8) []byte {
	b := make([]byte, 18) // 4-byte header + 14-byte payload
	b[0] = 0x0C           // GIP_CMD_FIRMWARE
	b[1] = GIPFlagSystem  // 0x20
	b[2] = seq
	b[3] = 14 // payload length

	b[4] = 0x01 // subtype: firmware-version announcement
	// bytes 5-9 reserved (zero)
	binary.LittleEndian.PutUint16(b[10:12], 5)  // major — keep in sync with Hello + metadata
	binary.LittleEndian.PutUint16(b[12:14], 21) // minor
	binary.LittleEndian.PutUint16(b[14:16], 0)  // build
	binary.LittleEndian.PutUint16(b[16:18], 0)  // revision
	return b
}

//nolint:unused // protocol reference for the unfinished GIP work
func appendGIPLEB128(dst []byte, v int) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		dst = append(dst, b)
		if v == 0 {
			return dst
		}
	}
}

// appendGIPLen2 writes a value using a forced 2-byte 7-bit-extended
// encoding (low byte with bit 7 set, high byte 0). Required for GIP
// Total Length and fragment Offset fields per MS-GIPUSB — those fields
// have a fixed 2-byte width regardless of value magnitude. Using
// variable-width LEB128 (single byte when v < 128) causes the host's
// parser to consume the following byte as the offset's high part,
// landing on bogus offsets and dropping the fragment.
//
// 2026-05-19: confirmed by host-side ACK trace — sending fragment 2
// with offset 58 as the single byte 0x3A made the host parse offset
// as 58 + (data[0] << 7) ~= 2106, far past the blob end. Fragments
// 2-4 silently rejected; ACK reported "received 58 of 216 bytes".
func appendGIPLen2(dst []byte, v int) []byte {
	lo := byte(0x80 | (v & 0x7F))
	hi := byte((v >> 7) & 0x7F)
	return append(dst, lo, hi)
}

// fragmentMetadata splits the metadata blob into GIP fragmented
// packets for transport over the 64-byte interrupt endpoint.
// Each fragment is a complete GIP packet ready for EP IN.
func fragmentMetadata(seq uint8) [][]byte {
	data := gipMetadata[:]
	total := len(data)
	var fragments [][]byte

	offset := 0
	first := true
	for offset < total {
		payloadSize := gipFragMaxPayload
		if remaining := total - offset; remaining < payloadSize {
			payloadSize = remaining
		}

		header := make([]byte, 0, 6)
		header = append(header, GIPDescriptor)

		if first {
			// First fragment: Fragment + InitFrag + System + ACME.
			// Total Length is a forced 2-byte field; see appendGIPLen2.
			header = append(header,
				GIPFlagFragment|GIPFlagInitFrag|GIPFlagSystem|GIPFlagACME, // 0xF0
				seq,
				byte(payloadSize), // this fragment's payload length
			)
			header = appendGIPLen2(header, total)
			first = false
		} else {
			// Subsequent fragments: Fragment + System (+ ACME on last).
			// Fragment Offset is a forced 2-byte field — variable-width
			// LEB128 would let the host pull data bytes into the offset
			// field for offsets < 128 (verified via ACK trace 2026-05-19).
			flags := byte(GIPFlagFragment | GIPFlagSystem) // 0xA0
			if offset+payloadSize >= total {
				flags |= GIPFlagACME // final fragment needs ACK
			}
			header = append(header,
				flags,
				seq, // same sequence as first fragment
				byte(payloadSize),
			)
			header = appendGIPLen2(header, offset)
		}

		pkt := make([]byte, len(header)+payloadSize)
		copy(pkt, header)
		copy(pkt[len(header):], data[offset:offset+payloadSize])
		fragments = append(fragments, pkt)
		offset += payloadSize
	}

	return fragments
}

// buildMetadataComplete builds the zero-payload "Metadata Complete" packet
// that follows the final data fragment of a chunked metadata response, per
// MS-GIPUSB §2.1.1.3. The host expects this after seeing the final fragment
// (ACME-flagged) to close out the metadata transaction; without it the
// xboxgip.sys state machine considers the metadata transfer incomplete and
// does not advance to publishing the IGamepad child PDO.
//
// Wire format:
//
//	04 A0 <seq> 00 <totalLen LEB128>
//
// Same sequence as the data fragments.
func buildMetadataComplete(seq uint8, totalLen int) []byte {
	pkt := []byte{
		GIPDescriptor,                   // 0x04
		GIPFlagFragment | GIPFlagSystem, // 0xA0 (no ACME, no InitFrag)
		seq,                             // same seq as the data fragments
		0x00,                            // zero payload
	}
	pkt = appendGIPLen2(pkt, totalLen)
	return pkt
}

// gipInputReportElite2Size matches what the kernel xbox_gip.c
// XBE2_5 paddle layout expects for Xbox Elite 2 wired firmware 5.17+:
// 4-byte header + 42-byte payload = 46 bytes total. Paddle bits live
// at payload offset 14 (= packet byte 18). Profile-slot byte lives
// at payload offset 20 (= packet byte 24) — MUST be 0 for paddles
// to be surfaced raw rather than suppressed as "remapped".
const gipInputReportElite2Size = 46
const gipInputPayloadElite2Size = 42

// remapPaddlesToEliteBits translates the helper-convention paddle byte
// (P1=bit0..P4=bit3) into the kernel-expected layout per xbox_gip.c
// gip_handle_ll_input_report XBE2_5 branch:
//
//	BIT(2) = P1 (upper-right)
//	BIT(0) = P2 (lower-right)
//	BIT(3) = P3 (upper-left)
//	BIT(1) = P4 (lower-left)
func remapPaddlesToEliteBits(helperPaddles byte) byte {
	var out byte
	if helperPaddles&0x01 != 0 { // helper P1 → upper-right
		out |= 1 << 2
	}
	if helperPaddles&0x02 != 0 { // helper P2 → lower-right
		out |= 1 << 0
	}
	if helperPaddles&0x04 != 0 { // helper P3 → upper-left
		out |= 1 << 3
	}
	if helperPaddles&0x08 != 0 { // helper P4 → lower-left
		out |= 1 << 1
	}
	return out
}

// buildInputReportElite2 emits a 46-byte input report carrying the
// standard 14-byte gamepad payload plus Elite-2 firmware-5.x paddle
// byte and profile-slot byte.
//
// Layout matches kernel xbox_gip.c XBE2_5 (fw 5.17+):
//
//	payload[0..13]   = standard gamepad (buttons, triggers, sticks)
//	payload[14]      = paddle bits  (BIT(2)=P1 UR, BIT(0)=P2 LR,
//	                                 BIT(3)=P3 UL, BIT(1)=P4 LL)
//	payload[15..19]  = reserved (zero)
//	payload[20]      = profile-slot byte (low 2 bits). MUST be 0 to
//	                    enable paddles raw; non-zero suppresses them.
//	payload[21..41]  = reserved (zero)
//
// In the full packet (with 4-byte GIP header) these are at bytes 18,
// 19..23, 24, 25..45 respectively.
func buildInputReportElite2(state *InputState, seq uint8, paddles, mode byte) []byte {
	b := make([]byte, gipInputReportElite2Size)
	b[0] = GIPInput
	b[1] = 0x00
	b[2] = seq
	b[3] = gipInputPayloadElite2Size

	buttons := xinputToGipButtons(state.Buttons)
	binary.LittleEndian.PutUint16(b[4:6], buttons)
	binary.LittleEndian.PutUint16(b[6:8], triggerU8ToU10(state.LT))
	binary.LittleEndian.PutUint16(b[8:10], triggerU8ToU10(state.RT))
	binary.LittleEndian.PutUint16(b[10:12], uint16(state.LX))
	binary.LittleEndian.PutUint16(b[12:14], uint16(state.LY))
	binary.LittleEndian.PutUint16(b[14:16], uint16(state.RX))
	binary.LittleEndian.PutUint16(b[16:18], uint16(state.RY))

	// payload byte 14 = paddle byte
	b[18] = remapPaddlesToEliteBits(paddles)
	// payload bytes 15..19 = reserved zero
	// payload byte 20 = profile slot — keep 0 so kernel doesn't mute paddles
	b[24] = mode & 0x03

	return b
}

// buildUnmappedStatePacket emits the GIP 0x0C UNMAPPED_STATE message
// that Elite 2 firmware 5.11-5.16 sends to carry paddle state on the
// XBE2_RAW path. Not used on XBE2_5 (fw 5.17+) but kept for the case
// where xboxgip.sys / dc1-controller.inf interrogates this message
// type. Layout per kernel xbox_gip.c gip_handle_command_raw_report.
func buildUnmappedStatePacket(seq uint8, paddles, mode byte) []byte {
	b := make([]byte, 20)
	b[0] = 0x0C
	b[1] = 0x00
	b[2] = seq
	b[3] = 16
	// paddle byte at payload byte 14 (= data[18] in kernel) for XBE2_RAW
	b[18] = remapPaddlesToEliteBits(paddles)
	b[19] = mode & 0x03
	return b
}

// buildVirtualKeyPacket emits a GIP VirtualKey (0x07) packet for the
// Guide button. Payload: {state_bits, key_code}. Kernel xbox_gip.c
// gip_handle_command_guide_button_status checks `bytes[1] == VK_LWIN`
// (0x5B) and ignores other key codes. Pressed/released bit comes from
// `bytes[0] & 3` so bit 0 represents the down state.
func buildVirtualKeyPacket(seq uint8, down bool, keyCode byte) []byte {
	b := make([]byte, 6)
	b[0] = GIPVirtualKey // 0x07
	b[1] = GIPFlagSystem // 0x20
	b[2] = seq
	b[3] = 0x02 // payload length = 2
	if down {
		b[4] = 0x01
	} else {
		b[4] = 0x00
	}
	b[5] = keyCode
	return b
}

// Guide button key code per xone/SDL: VK_LWIN.
const gipVKeyLeftWin = 0x5B

// buildInputReport builds an 18-byte GIP gamepad input report from InputState.
// Format: [0x20, 0x00, seq, 0x0E, buttons_lo, buttons_hi, LT_lo, LT_hi,
//
//	RT_lo, RT_hi, LX_lo, LX_hi, LY_lo, LY_hi, RX_lo, RX_hi, RY_lo, RY_hi]
func buildInputReport(state *InputState, seq uint8) []byte {
	b := make([]byte, gipInputReportSize)
	// GIP header
	b[0] = GIPInput // 0x20
	b[1] = 0x00     // flags: non-system, no ACK
	b[2] = seq
	b[3] = gipInputPayloadSize // 0x0E = 14

	// Buttons: remap XInput → GIP
	buttons := xinputToGipButtons(state.Buttons)
	binary.LittleEndian.PutUint16(b[4:6], buttons)

	// Triggers: scale 0-255 → 0-1023 (10-bit per MS-GIPUSB spec)
	binary.LittleEndian.PutUint16(b[6:8], triggerU8ToU10(state.LT))
	binary.LittleEndian.PutUint16(b[8:10], triggerU8ToU10(state.RT))

	// Sticks: pass through as-is (int16 LE)
	binary.LittleEndian.PutUint16(b[10:12], uint16(state.LX))
	binary.LittleEndian.PutUint16(b[12:14], uint16(state.LY))
	binary.LittleEndian.PutUint16(b[14:16], uint16(state.RX))
	binary.LittleEndian.PutUint16(b[16:18], uint16(state.RY))

	return b
}

// buildAcknowledge builds a GIP ACK message for a received command.
//
//nolint:unused // protocol reference for the unfinished GIP work
func buildAcknowledge(origSeq uint8, origCmd uint8, bytesReceived uint16) []byte {
	b := make([]byte, 13)
	b[0] = GIPAcknowledge // 0x01
	b[1] = GIPFlagSystem  // 0x20
	b[2] = origSeq        // match the sequence being acknowledged
	b[3] = 0x09           // payload length = 9
	b[4] = 0x00           // unknown
	b[5] = origCmd        // inner command ID being acknowledged
	b[6] = 0x20           // inner client/flags
	binary.LittleEndian.PutUint16(b[7:9], bytesReceived)
	b[9] = 0x00                                // unknown
	b[10] = 0x00                               // unknown
	binary.LittleEndian.PutUint16(b[11:13], 0) // remaining buffer
	return b
}
