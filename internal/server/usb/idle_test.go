package usb

import (
	"testing"

	"github.com/Alia5/VIIPER/usb"
)

type deviceWideIdle struct{ usb.Device }

func (deviceWideIdle) NaksWhenIdle() bool { return true }

type compositeIdle struct{ usb.Device }

func (compositeIdle) NaksWhenIdleForEndpoint(ep uint32) bool { return ep != 3 }
func (compositeIdle) NaksWhenIdle() bool                     { return true }

func TestInterruptInIdlePolicy(t *testing.T) {
	deck := compositeIdle{}
	for _, test := range []struct {
		name string
		dev  usb.Device
		ep   uint32
		mode string
		want bool
	}{
		{"deck keyboard", deck, 1, "auto", true},
		{"deck mouse", deck, 2, "auto", true},
		{"deck controller", deck, 3, "auto", false},
		{"unset mode", deck, 1, "", true},
		{"explicit keepalive", deck, 1, "keepalive", false},
		{"explicit nak", deck, 3, "nak", true},
		{"device-wide policy", deviceWideIdle{}, 3, "auto", true},
		{"device-wide override", deviceWideIdle{}, 3, "keepalive", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := interruptInNAKIdle(test.dev, test.ep, test.mode); got != test.want {
				t.Fatalf("NAK idle = %v, want %v", got, test.want)
			}
		})
	}
}
