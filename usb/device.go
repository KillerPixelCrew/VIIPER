package usb

import "context"

// Device is the minimal interface a device must implement.
// It only handles non-EP0 (interrupt/bulk) transfers.
type Device interface {
	// HandleTransfer processes a non-EP0 transfer (interrupt/bulk).
	// ep is the endpoint number (without direction). dir is usbip.DirIn or usbip.DirOut.
	// For IN transfers the implementation should block until data is available or ctx is
	// cancelled, then return the payload; returning nil means no data for this poll. For OUT
	// transfers, consume 'out' and return nil.
	HandleTransfer(ctx context.Context, ep uint32, dir uint32, out []byte) []byte
	GetDescriptor() *Descriptor
	GetDeviceSpecificArgs() map[string]any
}

// InterruptInSource is an optional interface for devices whose interrupt-IN endpoints can be
// served without a call per poll. The server then owns the poll timer and the completion frame:
// an endpoint whose state has not changed since its last report is completed by sending that
// report again, with no allocation, no context per URB and no call into the device.
//
// A device that implements it keeps HandleTransfer for its OUT endpoints, for bulk endpoints and
// for any interrupt-IN endpoint it declines here.
type InterruptInSource interface {
	// InputSignal returns the endpoint's fresh-input channel. A receive means the endpoint's
	// state changed since its last report was built, and the channel must coalesce: any number
	// of changes between two receives collapse into one. A nil channel means the endpoint never
	// produces input of its own, so its URBs stay pending until the poll interval or teardown;
	// returning nil for an endpoint the device does serve through HandleTransfer requires the
	// endpoint to NAK when idle, otherwise the server uses HandleTransfer for it instead.
	InputSignal(ep uint32) <-chan struct{}
	// WriteInputReport encodes the endpoint's current input state into buf and returns its
	// length. false means the endpoint has nothing to report right now, and the URB stays
	// pending.
	WriteInputReport(ep uint32, buf []byte) (int, bool)
}

// ControlDevice is an optional interface for devices that need to handle
// control transfers on endpoint 0 (EP0).
//
// This is primarily used for class-specific requests that are not covered by
// the server's built-in standard request handling (e.g. HID GET_REPORT/
// SET_REPORT).
type ControlDevice interface {
	// HandleControl handles a control request.
	//
	// - bmRequestType, bRequest, wValue, wIndex, wLength are the raw setup packet fields.
	// - data is the OUT data stage payload (for host-to-device requests), and is nil for
	//   device-to-host requests.
	//
	// If handled is false, the server will fall back to its default behavior.
	// If handled is true, the returned bytes (if any) will be used as the IN data stage.
	HandleControl(bmRequestType, bRequest uint8, wValue, wIndex, wLength uint16, data []byte) (resp []byte, handled bool)
}
