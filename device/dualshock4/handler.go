package dualshock4

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"

	"github.com/Alia5/VIIPER/device"
	"github.com/Alia5/VIIPER/internal/server/api"
	"github.com/Alia5/VIIPER/usb"
)

func init() {
	api.RegisterDevice("dualshock4", &handler{})
}

type handler struct{}

// serials holds the serial numbers of DualShock 4 devices in use.
var serials = device.NewIdentityPool()

func (h *handler) CreateDevice(o *device.CreateOptions) (usb.Device, error) {
	if o == nil {
		o = &device.CreateOptions{}
	}

	metaState := MetaState{}
	if o.DeviceSpecific != "" {
		if err := json.Unmarshal([]byte(o.DeviceSpecific), &metaState); err != nil {
			return nil, fmt.Errorf("invalid device specific JSON: %w", err)
		}
	}
	serial := DefaultSerialString
	if metaState.SerialNumber != "" {
		serial = metaState.SerialNumber
	}
	// The wire format is 16 hex digits; zero-pad, as space padding does not decode.
	if len(serial) < 16 {
		serial = strings.Repeat("0", 16-len(serial)) + serial
	}
	metaState.SerialNumber = serials.Reserve(serial)
	b, err := json.Marshal(metaState)
	if err != nil {
		return nil, fmt.Errorf("marshal meta state: %w", err)
	}
	o.DeviceSpecific = string(b)
	d, err := New(o)
	if err != nil {
		serials.Release(metaState.SerialNumber)
		return nil, err
	}
	// Held for as long as the device is on the bus, not for one stream.
	reserved := metaState.SerialNumber
	d.OnRelease(func() { serials.Release(reserved) })
	return d, nil
}

func (h *handler) StreamHandler() api.StreamHandlerFunc {
	return func(conn net.Conn, devPtr *usb.Device, logger *slog.Logger) error {
		if devPtr == nil || *devPtr == nil {
			return fmt.Errorf("nil device")
		}
		ds4, ok := (*devPtr).(*DualShock4)
		if !ok {
			return fmt.Errorf("%w: expected DualShock4", device.ErrWrongDeviceType)
		}

		ds4.SetOutputCallback(func(feedback OutputState) {
			data, err := feedback.MarshalBinary()
			if err != nil {
				logger.Error("failed to marshal feedback", "error", err)
				return
			}
			if _, err := conn.Write(data); err != nil {
				logger.Error("failed to send feedback", "error", err)
			}
		})

		buf := make([]byte, 31)
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
			ds4.UpdateInputState(&state)
		}
	}
}

func (h *handler) UpdateMetaState(meta string, dev *usb.Device) error {
	ds4, ok := (*dev).(*DualShock4)
	if !ok {
		return fmt.Errorf("%w: expected DualShock4", device.ErrWrongDeviceType)
	}
	var metaState MetaState
	err := json.Unmarshal([]byte(meta), &metaState)
	if err != nil {
		return fmt.Errorf("unmarshal meta state: %w", err)
	}
	ds4.SetMetaState(metaState)

	return nil
}
