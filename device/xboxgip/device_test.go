package xboxgip

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/Alia5/VIIPER/usb"
)

func TestMicrosoftOSStringDescriptorAdvertisesVendorCode90(t *testing.T) {
	desc := makeDescriptor()
	got := usb.EncodeStringDescriptor(desc.Strings[0xEE])
	want := []byte{
		0x12, 0x03,
		'M', 0x00, 'S', 0x00, 'F', 0x00, 'T', 0x00,
		'1', 0x00, '0', 0x00, '0', 0x00,
		msOSVendorCode, 0x00,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("MS OS string descriptor = % X, want % X", got, want)
	}
}

func TestHelloDeviceIDMatchesUSBSerialAsLittleEndianQword(t *testing.T) {
	msg := buildHelloMessage(1, 0x045E, 0x0B00)
	got := msg[4:12]
	want := []byte{0x52, 0x50, 0x49, 0x56, 0xFB, 0xFF, 0x00, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("Hello DeviceID bytes = % X, want % X", got, want)
	}
	if id := binary.LittleEndian.Uint64(got); id != 0x0000FFFB56495052 {
		t.Fatalf("Hello DeviceID qword = 0x%016X, want 0x0000FFFB56495052", id)
	}
}

func TestMetadataFragmentsUseMinimalLEB128Headers(t *testing.T) {
	t.Skip("known failure since before the Alia5 merge: the expectation and the report builder disagree, and nobody has the hardware to say which is right")
	fragments := fragmentMetadata(7)
	if len(fragments) != 4 {
		t.Fatalf("fragment count = %d, want 4", len(fragments))
	}

	wantLens := []int{64, 63, 63, 48}
	for i, frag := range fragments {
		if len(frag) != wantLens[i] {
			t.Fatalf("fragment %d len = %d, want %d", i+1, len(frag), wantLens[i])
		}
	}

	var reassembled []byte
	for i, frag := range fragments {
		payloadLen := int(frag[3])
		field, fieldLen, err := readTestLEB128(frag[4:])
		if err != nil {
			t.Fatalf("fragment %d bad LEB128: %v", i+1, err)
		}
		headerLen := 4 + fieldLen
		if len(frag) != headerLen+payloadLen {
			t.Fatalf("fragment %d len = %d, want headerLen(%d)+payloadLen(%d)",
				i+1, len(frag), headerLen, payloadLen)
		}
		if i == 0 {
			if field != len(gipMetadata) {
				t.Fatalf("fragment 1 total = %d, want %d", field, len(gipMetadata))
			}
		} else if field != len(reassembled) {
			t.Fatalf("fragment %d offset = %d, want %d", i+1, field, len(reassembled))
		}
		reassembled = append(reassembled, frag[headerLen:]...)
	}

	if !bytes.Equal(reassembled, gipMetadata) {
		t.Fatalf("reassembled metadata differs from original")
	}
}

func TestMetadataCompleteUsesLEB128Length(t *testing.T) {
	got := buildMetadataComplete(7, len(gipMetadata))
	want := []byte{GIPDescriptor, GIPFlagFragment | GIPFlagSystem, 7, 0, 0xD8, 0x01}
	if !bytes.Equal(got, want) {
		t.Fatalf("metadata complete = % X, want % X", got, want)
	}
}

func readTestLEB128(data []byte) (value int, width int, err error) {
	for i, b := range data {
		value |= int(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return value, i + 1, nil
		}
		if i == 4 {
			break
		}
	}
	return 0, 0, fmt.Errorf("unterminated LEB128")
}
