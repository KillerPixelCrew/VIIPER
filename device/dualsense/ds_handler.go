package dualsense

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
	api.RegisterDevice("dualsense", &dshandler{})
}

type dshandler struct{}

func (h *dshandler) CreateDevice(o *device.CreateOptions) (usb.Device, error) {
	if o == nil {
		o = &device.CreateOptions{}
	}

	metaState := MetaState{
		ShellColor: DefaultShellColor,
	}
	if o.DeviceSpecific != "" {
		if err := json.Unmarshal([]byte(o.DeviceSpecific), &metaState); err != nil {
			return nil, fmt.Errorf("invalid device specific JSON: %w", err)
		}
	}

	serial := metaState.SerialNumber
	if serial == "" {
		serial = DefaultSerialNumberDS
	}
	serial = applyShellColor(serial, metaState.ShellColor)
	metaState.SerialNumber = serials.Reserve(serial)

	mac := metaState.MACAddress
	if mac == "" {
		mac = DefaultMACAddressDS
	}
	metaState.MACAddress = macs.Reserve(mac)

	b, err := json.Marshal(metaState)
	if err != nil {
		return nil, fmt.Errorf("marshal meta state: %w", err)
	}
	o.DeviceSpecific = string(b)

	d, err := new(o, false)
	if err != nil {
		serials.Release(metaState.SerialNumber)
		macs.Release(metaState.MACAddress)
		return nil, err
	}
	// Identities are held for as long as the device is on the bus, not for
	// the life of one stream: a client may reconnect to the same device.
	reservedSerial, reservedMAC := metaState.SerialNumber, metaState.MACAddress
	d.OnRelease(func() {
		serials.Release(reservedSerial)
		macs.Release(reservedMAC)
	})
	return d, nil
}

func (h *dshandler) StreamHandler() api.StreamHandlerFunc {
	return func(conn net.Conn, devPtr *usb.Device, logger *slog.Logger) error {
		if devPtr == nil || *devPtr == nil {
			return fmt.Errorf("nil device")
		}
		dse, ok := (*devPtr).(*DualSense)
		if !ok {
			return fmt.Errorf("%w: expected DualSenseEdge", device.ErrWrongDeviceType)
		}

		dse.SetOutputCallback(func(feedback OutputState) {
			data, err := feedback.MarshalBinary()
			if err != nil {
				logger.Error("failed to marshal feedback", "error", err)
				return
			}
			if _, err := conn.Write(data); err != nil {
				logger.Error("failed to send feedback", "error", err)
			}
		})

		buf := make([]byte, InputStateSize)
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
			dse.UpdateInputState(&state)
		}
	}
}

func (h *dshandler) UpdateMetaState(meta string, dev *usb.Device) error {
	dse, ok := (*dev).(*DualSense)
	if !ok {
		return fmt.Errorf("%w: expected DualSenseEdge", device.ErrWrongDeviceType)
	}
	dse.mtx.Lock()
	current := *dse.metaState
	dse.mtx.Unlock()
	if err := json.Unmarshal([]byte(meta), &current); err != nil {
		return fmt.Errorf("unmarshal meta state: %w", err)
	}
	dse.SetMetaState(current)
	return nil
}
