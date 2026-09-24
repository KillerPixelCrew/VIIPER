package xboxelite2

import (
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/Alia5/VIIPER/device"
	elite2state "github.com/Alia5/VIIPER/internal/inputstate/elite2"
	"github.com/Alia5/VIIPER/internal/server/api"
	"github.com/Alia5/VIIPER/usb"
)

func init() {
	api.RegisterDevice("xboxelite2", &handler{})
}

type handler struct{}

func (h *handler) CreateDevice(o *device.CreateOptions) (usb.Device, error) { return New(o) }

func (h *handler) StreamHandler() api.StreamHandlerFunc {
	return func(conn net.Conn, devPtr *usb.Device, logger *slog.Logger) error {
		if devPtr == nil || *devPtr == nil {
			return fmt.Errorf("nil device")
		}
		xdev, ok := (*devPtr).(*XboxElite2)
		if !ok {
			return fmt.Errorf("device is not xboxelite2")
		}

		xdev.SetOutputCallback(func(feedback elite2state.OutputState) {
			data, err := feedback.MarshalBinary()
			if err != nil {
				logger.Error("failed to marshal feedback", "error", err)
				return
			}
			if _, err := conn.Write(data); err != nil {
				logger.Error("failed to send feedback", "error", err)
			}
		})

		// Support v2 (33-byte: IMU + Steam touch), v1 (26-byte: IMU), and legacy (14-byte).
		// This prevents hard-to-debug desync ghost input when client/server versions differ.
		frameSize := 0
		readBuf := make([]byte, 512)
		pending := make([]byte, 0, elite2state.InputStateSize*8)

		for {
			n, err := conn.Read(readBuf)
			if err != nil {
				if err == io.EOF {
					logger.Info("client disconnected")
					return nil
				}
				return fmt.Errorf("read input state: %w", err)
			}
			if n <= 0 {
				continue
			}
			pending = append(pending, readBuf[:n]...)

			if frameSize == 0 {
				if detected, ok := detectInputFrameSize(pending); ok {
					frameSize = detected
					switch frameSize {
					case elite2state.LegacyInputStateSize:
						logger.Warn("xboxelite2: detected legacy 14-byte stream; IMU forwarding disabled for this session")
					case elite2state.InputStateV1Size:
						logger.Info("xboxelite2: detected 26-byte stream with IMU")
					default:
						logger.Info("xboxelite2: detected 33-byte stream with IMU + Steam touchpad")
					}
				} else if len(pending) > elite2state.InputStateSize*128 {
					// Keep memory bounded while waiting for enough data to detect framing.
					pending = pending[len(pending)-elite2state.InputStateSize*128:]
				}
			}

			for frameSize > 0 && len(pending) >= frameSize {
				frame := pending[:frameSize]
				pending = pending[frameSize:]

				var state elite2state.InputState
				var unmarshalErr error
				switch frameSize {
				case elite2state.LegacyInputStateSize:
					unmarshalErr = state.UnmarshalLegacyBinary(frame)
				case elite2state.InputStateV1Size:
					unmarshalErr = state.UnmarshalV1Binary(frame)
				default:
					unmarshalErr = state.UnmarshalBinary(frame)
				}
				if unmarshalErr != nil {
					return fmt.Errorf("unmarshal input state: %w", unmarshalErr)
				}
				xdev.UpdateInputState(&state)
			}
		}
	}
}

func detectInputFrameSize(pending []byte) (int, bool) {
	legacyScore, legacyFrames := scoreFrameSize(pending, elite2state.LegacyInputStateSize)
	v1Score, v1Frames := scoreFrameSize(pending, elite2state.InputStateV1Size)
	v2Score, v2Frames := scoreFrameSize(pending, elite2state.InputStateSize)
	if legacyFrames == 0 || v1Frames == 0 || v2Frames == 0 {
		return 0, false
	}

	legacyRatio := float64(legacyScore) / float64(legacyFrames)
	v1Ratio := float64(v1Score) / float64(v1Frames)
	v2Ratio := float64(v2Score) / float64(v2Frames)

	// Require a clear winner to avoid locking in the wrong frame size.
	if v2Ratio >= 0.80 && v2Ratio >= v1Ratio+0.10 && v2Ratio >= legacyRatio+0.10 {
		return elite2state.InputStateSize, true
	}
	if v1Ratio >= 0.80 && v1Ratio >= v2Ratio+0.10 && v1Ratio >= legacyRatio+0.10 {
		return elite2state.InputStateV1Size, true
	}
	if legacyRatio >= 0.80 && legacyRatio >= v1Ratio+0.10 && legacyRatio >= v2Ratio+0.10 {
		return elite2state.LegacyInputStateSize, true
	}
	return 0, false
}

func scoreFrameSize(pending []byte, frameSize int) (score int, frames int) {
	const minFrames = 6
	const maxFrames = 24
	frames = len(pending) / frameSize
	if frames < minFrames {
		return 0, 0
	}
	if frames > maxFrames {
		frames = maxFrames
	}
	for i := 0; i < frames; i++ {
		start := i * frameSize
		end := start + frameSize
		if plausibleFrame(pending[start:end], frameSize) {
			score++
		}
	}
	return score, frames
}

func plausibleFrame(frame []byte, frameSize int) bool {
	if len(frame) < elite2state.LegacyInputStateSize {
		return false
	}
	dpad := frame[12]
	reserved := frame[13]
	if dpad&^DPadMask != 0 {
		return false
	}
	if reserved&^ReservedShare != 0 {
		return false
	}
	if frameSize == elite2state.InputStateSize {
		touchFlags := frame[26]
		if touchFlags&^(elite2state.TouchFlagRightPadTouch|elite2state.TouchFlagRightPadPress) != 0 {
			return false
		}
	}
	return true
}

func (h *handler) UpdateMetaState(meta string, dev *usb.Device) error {
	return nil
}
