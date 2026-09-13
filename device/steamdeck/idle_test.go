package steamdeck_test

import (
	"testing"

	"github.com/Alia5/VIIPER/device/steamdeck"
)

func TestOnlyPlaceholderEndpointsNAKWhenIdle(t *testing.T) {
	deck, err := steamdeck.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range []uint32{1, 2, 3, 4} {
		if got, want := deck.NaksWhenIdleForEndpoint(ep), ep == 1 || ep == 2; got != want {
			t.Errorf("endpoint %d NAK idle = %v, want %v", ep, got, want)
		}
	}
}
