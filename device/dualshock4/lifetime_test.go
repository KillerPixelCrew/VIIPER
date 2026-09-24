package dualshock4

import (
	"testing"

	"github.com/Alia5/VIIPER/device"
	"github.com/Alia5/VIIPER/virtualbus"
)

func TestSerialIsHeldUntilTheBusRemovesTheDevice(t *testing.T) {
	serials = device.NewIdentityPool()
	t.Cleanup(func() { serials = device.NewIdentityPool() })

	bus, err := virtualbus.NewWithBusID(9101)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	h := &handler{}
	first, err := h.CreateDevice(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bus.Add(first); err != nil {
		t.Fatal(err)
	}
	firstSerial := first.(*DualShock4).metaState.SerialNumber

	second, err := h.CreateDevice(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.(*DualShock4).metaState.SerialNumber; got == firstSerial {
		t.Fatalf("second device reused %q while the first is on the bus", got)
	}

	if err := bus.Remove(first); err != nil {
		t.Fatal(err)
	}
	third, err := h.CreateDevice(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := third.(*DualShock4).metaState.SerialNumber; got != firstSerial {
		t.Fatalf("serial %q was not released on removal, got %q", firstSerial, got)
	}
}

func TestShortSerialIsZeroPadded(t *testing.T) {
	serials = device.NewIdentityPool()
	t.Cleanup(func() { serials = device.NewIdentityPool() })

	dev, err := (&handler{}).CreateDevice(&device.CreateOptions{DeviceSpecific: `{"serial_number":"1A2B"}`})
	if err != nil {
		t.Fatal(err)
	}
	if got := dev.(*DualShock4).metaState.SerialNumber; got != "0000000000001A2B" {
		t.Fatalf("serial = %q, want 0000000000001A2B", got)
	}
}
