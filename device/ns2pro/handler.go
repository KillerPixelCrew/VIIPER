package ns2pro

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/Alia5/VIIPER/device"
	"github.com/Alia5/VIIPER/internal/server/api"
	"github.com/Alia5/VIIPER/usb"
)

func init() {
	api.RegisterDevice("ns2pro", &handler{})
}

type handler struct{}

// serials holds the serial numbers of NS2 Pro devices in use.
var serials = device.NewIdentityPool()

func (h *handler) CreateDevice(o *device.CreateOptions) (usb.Device, error) {
	if o == nil {
		o = &device.CreateOptions{}
	}

	metaState := *defaultMetaState()
	if o.DeviceSpecific != "" {
		if err := json.Unmarshal([]byte(o.DeviceSpecific), &metaState); err != nil {
			return nil, fmt.Errorf("invalid device specific JSON: %w", err)
		}
	}

	serial := metaState.SerialNumber
	if serial == "" {
		serial = DefaultSerial
	}
	metaState.SerialNumber = serials.Reserve(serial)

	b, err := json.Marshal(metaState)
	if err != nil {
		return nil, fmt.Errorf("marshal meta state: %w", err)
	}
	o.DeviceSpecific = string(b)

	return New(o)
}

func (h *handler) StreamHandler() api.StreamHandlerFunc {
	return func(conn net.Conn, devPtr *usb.Device, logger *slog.Logger) error {
		defer func() {
			if devPtr == nil || *devPtr == nil {
				return
			}
			ns2, ok := (*devPtr).(*NS2Pro)
			if !ok {
				slog.Warn("device is not ns2pro on disconnect")
				return
			}
			serial := ns2.serialNumber()
			if serial == "" {
				return
			}
			serials.Release(serial)
			slog.Debug("ns2pro disconnected, serial released", "serial", serial)
		}()

		if devPtr == nil || *devPtr == nil {
			return fmt.Errorf("nil device")
		}
		ns2, ok := (*devPtr).(*NS2Pro)
		if !ok {
			return fmt.Errorf("device is not ns2pro")
		}

		clearOutputCallback := ns2.SetOutputCallback(func(feedback OutputState) {
			data, err := feedback.MarshalBinary()
			if err != nil {
				logger.Error("failed to marshal ns2pro feedback", "error", err)
				return
			}
			if _, err := conn.Write(data); err != nil {
				logger.Error("failed to send ns2pro feedback", "error", err)
			}
		})
		defer clearOutputCallback()

		buf := make([]byte, InputWireSize)
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
			ns2.UpdateInputState(state)
		}
	}
}

func (h *handler) UpdateMetaState(meta string, dev *usb.Device) error {
	ns2, ok := (*dev).(*NS2Pro)
	if !ok {
		return fmt.Errorf("%w: expected ns2pro", device.ErrWrongDeviceType)
	}

	metaState := *defaultMetaState()
	if err := json.Unmarshal([]byte(meta), &metaState); err != nil {
		return fmt.Errorf("unmarshal meta state: %w", err)
	}

	ns2.SetMetaState(metaState)
	return nil
}
