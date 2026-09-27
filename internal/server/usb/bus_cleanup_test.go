package usb_test

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Alia5/VIIPER/device/steamdeck"
	usbsrv "github.com/Alia5/VIIPER/internal/server/usb"
	"github.com/Alia5/VIIPER/virtualbus"
)

// newCleanupServer builds a server whose empty-bus cleanup fires quickly, with one Steam Deck on
// bus 1, and returns the bus for the test to empty.
func newCleanupServer(t *testing.T, keepWhenEmpty bool) (*usbsrv.Server, *virtualbus.VirtualBus) {
	t.Helper()
	server := usbsrv.New(usbsrv.ServerConfig{
		Addr:              "127.0.0.1:0",
		ConnectionTimeout: 30 * time.Second,
		BusCleanupTimeout: 20 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	t.Cleanup(func() { _ = server.Close() })

	bus, err := virtualbus.NewWithBusID(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	bus.SetKeepWhenEmpty(keepWhenEmpty)
	deck, err := steamdeck.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bus.Add(deck); err != nil {
		t.Fatal(err)
	}
	if err := server.AddBus(bus); err != nil {
		t.Fatal(err)
	}
	return server, bus
}

// waitForCleanup gives the cleanup timer several times its timeout to fire.
func waitForCleanup() {
	time.Sleep(200 * time.Millisecond)
}

func TestEmptyBusIsRemovedAfterTheCleanupTimeout(t *testing.T) {
	server, _ := newCleanupServer(t, false)

	if err := server.RemoveDeviceByID(1, "1"); err != nil {
		t.Fatal(err)
	}
	waitForCleanup()

	if server.GetBus(1) != nil {
		t.Fatal("an unowned bus left empty should have been removed")
	}
}

func TestAnOwnedBusSurvivesBeingEmptyPastTheCleanupTimeout(t *testing.T) {
	// The C API's contract: a bus the caller created is there for the next device_add however
	// long the caller took to get to it. Six seconds between removing a virtual controller and
	// adding its replacement lost the bus on an MSI Claw (2026-09-27).
	server, bus := newCleanupServer(t, true)

	if err := server.RemoveDeviceByID(1, "1"); err != nil {
		t.Fatal(err)
	}
	waitForCleanup()

	if server.GetBus(1) != bus {
		t.Fatal("an owned bus must survive being empty")
	}
	deck, err := steamdeck.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bus.Add(deck); err != nil {
		t.Fatalf("adding to the kept bus: %v", err)
	}
}

func TestAnOwnedBusIsStillRemovedExplicitly(t *testing.T) {
	server, _ := newCleanupServer(t, true)

	if err := server.RemoveBus(1); err != nil {
		t.Fatal(err)
	}

	if server.GetBus(1) != nil {
		t.Fatal("RemoveBus must remove an owned bus regardless of the keep flag")
	}
}
