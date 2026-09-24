package switchpro

// packStick12 packs two int16 stick values (±32767) into 3 bytes of 12-bit format
// as used by the Switch Pro Controller.
//
// Each axis is scaled from signed int16 (center=0) to unsigned 12-bit (center=2048, range 0..4095).
// Y axis is NOT inverted here — the caller is responsible for axis orientation.
func packStick12(x, y int16) [3]byte {
	// Scale ±32767 → 0..4095 (center 2048)
	ux := uint16((int32(x) + 32768) * 4095 / 65535)
	uy := uint16((int32(y) + 32768) * 4095 / 65535)

	// Clamp to 12 bits
	if ux > 4095 {
		ux = 4095
	}
	if uy > 4095 {
		uy = 4095
	}

	return [3]byte{
		byte(ux & 0xFF),
		byte((ux>>8)&0x0F) | byte((uy&0x0F)<<4),
		byte(uy >> 4),
	}
}

// parseRumble extracts simplified left/right rumble amplitude from HD Rumble data.
// HD Rumble is 8 bytes: 4 bytes per actuator (left=0-3, right=4-7).
//
// Per actuator layout:
//
//	byte 0: high-band frequency (encoded)
//	byte 1: high-band amplitude (bits 0-6) + freq MSB (bit 7)
//	byte 2: low-band frequency (encoded)
//	byte 3: low-band amplitude (bits 0-6) + freq MSB (bit 7)
//
// Neutral/off state is 00 01 40 40 per motor (HF amp=1, LF amp=0x40 baseline).
func parseRumble(data []byte) (left, right uint8) {
	if len(data) < 8 {
		return 0, 0
	}

	amp := func(hfAmpByte, lfAmpByte byte) uint8 {
		// High-frequency amplitude: bits 0-6. Values 0-1 = off.
		hf := hfAmpByte & 0x7F
		if hf <= 1 {
			hf = 0
		}

		// Low-frequency amplitude: bits 0-6, baseline 0x40 = off.
		lf := lfAmpByte & 0x7F
		if lf <= 0x40 {
			lf = 0
		} else {
			lf -= 0x40
		}

		return maxU8(hf, lf) * 2
	}

	left = amp(data[1], data[3])  // bytes 1,3 of left motor
	right = amp(data[5], data[7]) // bytes 1,3 of right motor
	return
}

func maxU8(a, b uint8) uint8 {
	if a > b {
		return a
	}
	return b
}

// fakeMACForProfile returns a deterministic 6-byte MAC address that differs per profile,
// so a Joy-Con pair doesn't share the same address.
func fakeMACForProfile(profile string) [6]byte {
	switch profile {
	case ProfileJoyConLeft:
		return [6]byte{0x98, 0xB6, 0xE9, 0x00, 0x00, 0x02}
	case ProfileJoyConRight:
		return [6]byte{0x98, 0xB6, 0xE9, 0x00, 0x00, 0x03}
	default:
		return [6]byte{0x98, 0xB6, 0xE9, 0x00, 0x00, 0x01}
	}
}

// defaultStickCalibration returns factory-default stick calibration data.
// Format matches SPI flash layout at addresses 0x603D (left) and 0x6046 (right).
// Each is 9 bytes: [maxAboveCenter(3), center(3), minBelowCenter(3)]
// with 12-bit values packed in the same 3-byte format as stick data.
func defaultStickCalibration() [18]byte {
	var cal [18]byte

	// Left stick calibration (9 bytes at SPI 0x603D):
	// Max above center: X=+700, Y=+700
	// Center: X=2048, Y=2048
	// Min below center: X=-700, Y=-700
	// Pack as 12-bit pairs.
	copy(cal[0:3], packStickCal(700, 700))
	copy(cal[3:6], packStickCal(2048, 2048))
	copy(cal[6:9], packStickCal(700, 700))

	// Right stick calibration (9 bytes at SPI 0x6046):
	copy(cal[9:12], packStickCal(2048, 2048))
	copy(cal[12:15], packStickCal(700, 700))
	copy(cal[15:18], packStickCal(700, 700))

	return cal
}

func packStickCal(x, y uint16) []byte {
	return []byte{
		byte(x & 0xFF),
		byte((x>>8)&0x0F) | byte((y&0x0F)<<4),
		byte(y >> 4),
	}
}

// defaultIMUCalibration returns factory-default IMU calibration data.
// 24 bytes matching the SPI flash layout at 0x6020 per dekuNukem docs:
//
//	bytes 0-5:   Accel Origin X/Y/Z   (3x int16 LE) — zero-offset
//	bytes 6-11:  Accel Sensitivity X/Y/Z (3x int16 LE)
//	bytes 12-17: Gyro Offset X/Y/Z    (3x int16 LE) — zero-rate offset
//	bytes 18-23: Gyro Sensitivity X/Y/Z (3x int16 LE)
func defaultIMUCalibration() [24]byte {
	var cal [24]byte

	// Accel origin (bytes 0-5): all zero (no offset)

	// Accel sensitivity (bytes 6-11): 0x4000 = 16384 per axis.
	// SDL formula: accel_g = raw * 4.0 / (coeff - origin)
	// With coeff=16384, origin=0: accel_g = raw * 4.0 / 16384 = raw / 4096
	// BMI323 at ±8g: 4096 LSB/g → 1G raw = 4096 → accel_g = 1.0 ✓
	cal[6] = 0x00
	cal[7] = 0x40 // accel X sensitivity = 0x4000
	cal[8] = 0x00
	cal[9] = 0x40 // accel Y
	cal[10] = 0x00
	cal[11] = 0x40 // accel Z

	// Gyro offset (bytes 12-17): all zero (no zero-rate offset)

	// Gyro sensitivity (bytes 18-23): 0x3BF7 = 15335 per axis.
	// SDL formula: gyro_dps = (raw - offset) * 936.0 / (coeff - offset)
	// With coeff=15335, offset=0: gyro_dps = raw * 936.0 / 15335 = raw * 0.06104
	// BMI323 at ±2000dps: 16.384 LSB/dps → 32767 raw ≈ 2000 dps ✓
	cal[18] = 0xF7
	cal[19] = 0x3B // gyro X sensitivity = 0x3BF7
	cal[20] = 0xF7
	cal[21] = 0x3B // gyro Y
	cal[22] = 0xF7
	cal[23] = 0x3B // gyro Z

	return cal
}

// defaultAccelHorizontalOffset returns the accelerometer horizontal offset
// for gravity compensation when the controller rests flat.
// 6 bytes: 3x int16 LE (X, Y, Z).
// Per dekuNukem defaults: Pro=(-688, 0, 4038), JC-L=(350, 0, 4081), JC-R=(350, 0, -4081).
func defaultAccelHorizontalOffset(profile string) [6]byte {
	var buf [6]byte
	var x, y, z int16
	switch profile {
	case ProfileJoyConLeft:
		x, y, z = 350, 0, 4081
	case ProfileJoyConRight:
		x, y, z = 350, 0, -4081
	default: // Pro Controller
		x, y, z = -688, 0, 4038
	}
	buf[0] = byte(uint16(x) & 0xFF)
	buf[1] = byte(uint16(x) >> 8)
	buf[2] = byte(uint16(y) & 0xFF)
	buf[3] = byte(uint16(y) >> 8)
	buf[4] = byte(uint16(z) & 0xFF)
	buf[5] = byte(uint16(z) >> 8)
	return buf
}
