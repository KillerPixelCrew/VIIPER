package steamcontroller

import (
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/Alia5/VIIPER/device"
	"github.com/Alia5/VIIPER/internal/server/api"
	"github.com/Alia5/VIIPER/usb"
)

func init() {
	api.RegisterDevice("steamcontroller", &handler{})
}

type handler struct{}

func (h *handler) CreateDevice(o *device.CreateOptions) (usb.Device, error) { return New(o) }

func (h *handler) StreamHandler() api.StreamHandlerFunc {
	return func(conn net.Conn, devPtr *usb.Device, logger *slog.Logger) error {
		if devPtr == nil || *devPtr == nil {
			return fmt.Errorf("nil device")
		}
		controller, ok := (*devPtr).(*SteamController)
		if !ok {
			return fmt.Errorf("device is not steamcontroller")
		}

		controller.SetOutputCallback(func(feedback OutputState) {
			data, err := feedback.MarshalBinary()
			if err != nil {
				logger.Error("failed to marshal feedback", "error", err)
				return
			}
			if _, err := conn.Write(data); err != nil {
				logger.Error("failed to send feedback", "error", err)
			}
		})

		buf := make([]byte, InputReportLen)
		for {
			if _, err := io.ReadFull(conn, buf); err != nil {
				if err == io.EOF {
					logger.Info("client disconnected")
					return nil
				}
				return fmt.Errorf("read input state: %w", err)
			}

			var state InputState
			if err := state.UnmarshalBinary(buf); err != nil {
				return fmt.Errorf("unmarshal input state: %w", err)
			}
			controller.UpdateInputState(&state)
		}
	}
}

func (h *handler) UpdateMetaState(meta string, dev *usb.Device) error {
	return nil
}
