package steamdeck

import (
	"testing"

	"github.com/Alia5/VIIPER/virtualbus"
)

func TestDeckSerialsMatchUsbAndRemainDistinctUntilRemoval(t *testing.T) {
	bus := virtualbus.New(0)
	t.Cleanup(func() { _ = bus.Close() })
	newDeck := func() *SteamDeck {
		t.Helper()
		d, err := New(nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.ReleaseResources)
		if _, err := bus.Add(d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	serial := func(d *SteamDeck) string {
		t.Helper()
		desc := d.GetDescriptor()
		usbSerial := desc.Strings[desc.Device.ISerialNumber]
		if usbSerial == "" || usbSerial == "Keyboard" {
			t.Fatalf("invalid USB serial %q", usbSerial)
		}
		for _, attribute := range []byte{StringAttributeBoardSerial, StringAttributeUnitSerial} {
			request := []byte{FeatureGetStringAttribute, 1, attribute}
			if _, ok := d.HandleControl(0x21, 0x09, 0x0300, 0, uint16(len(request)), request); !ok {
				t.Fatal("serial request refused")
			}
			response, ok := d.HandleControl(0xa1, 0x01, 0x0300, 0, InputReportLen, nil)
			if !ok || len(response) < 3+len(usbSerial) || response[2] != attribute || string(response[3:3+int(response[1])]) != usbSerial {
				t.Fatalf("attribute %d differs from USB serial %q: %x", attribute, usbSerial, response)
			}
		}
		return usbSerial
	}
	first, second := newDeck(), newDeck()
	firstSerial, secondSerial := serial(first), serial(second)
	if firstSerial == secondSerial {
		t.Fatalf("two live Decks share %q", firstSerial)
	}
	if err := bus.Remove(first); err != nil {
		t.Fatal(err)
	}
	// A second release must not free the serial after a replacement has claimed it.
	replacement := newDeck()
	first.ReleaseResources()
	if got := serial(replacement); got != firstSerial {
		t.Fatalf("replacement serial = %q, want %q", got, firstSerial)
	}
	if got := serial(newDeck()); got == firstSerial || got == secondSerial {
		t.Fatalf("another live Deck reused %q", got)
	}
}

func TestDeckEndpointMatchesSdlSensorClock(t *testing.T) {
	d, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.ReleaseResources)
	found := false
	for _, iface := range d.GetDescriptor().Interfaces {
		for _, endpoint := range iface.Endpoints {
			if endpoint.BEndpointAddress == 0x80|controllerEndpointNumber {
				found = true
				if endpoint.BInterval != 4 {
					t.Fatalf("Deck interval = %d ms, SDL advances by 4 ms", endpoint.BInterval)
				}
			}
		}
	}
	if !found {
		t.Fatal("Deck controller endpoint missing")
	}
}
