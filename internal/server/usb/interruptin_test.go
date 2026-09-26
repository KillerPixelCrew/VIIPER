package usb_test

import (
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/Alia5/VIIPER/device/steamdeck"
	usbsrv "github.com/Alia5/VIIPER/internal/server/usb"
	"github.com/Alia5/VIIPER/usbip"
	"github.com/Alia5/VIIPER/virtualbus"
)

// These cover the data-driven interrupt-IN path the Steam Deck is served by: a URB is in flight
// while it is in the server's pending map rather than while a context per URB is unexpired, a
// paced endpoint completes on its bInterval grid with the state it has, and the repeat of a
// report the host already has can be paced separately from the endpoint's bInterval.

const (
	deckControllerEndpoint = 3
	deckKeyboardEndpoint   = 1
	// The Steam Deck's endpoints declare a 6 ms bInterval.
	deckInterval = 6 * time.Millisecond
)

type deckStream struct {
	t      *testing.T
	conn   net.Conn
	deck   *steamdeck.SteamDeck
	nextIn uint32
}

// newDeckStream starts a server with one Steam Deck on it and imports the device, leaving a
// connection the test can submit URBs on.
func newDeckStream(t *testing.T, idleKeepalive time.Duration) *deckStream {
	t.Helper()
	server := usbsrv.New(usbsrv.ServerConfig{
		Addr:                     "127.0.0.1:0",
		ConnectionTimeout:        30 * time.Second,
		BusCleanupTimeout:        5 * time.Second,
		HardwarePacedCompletions: true,
		IdleMode:                 "auto",
		IdleKeepaliveInterval:    idleKeepalive,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	go func() { _ = server.ListenAndServe() }()
	<-server.Ready()
	if err := server.ReadyErr(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	bus, err := virtualbus.NewWithBusID(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
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

	conn, err := net.Dial("tcp", server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := (&usbip.MgmtHeader{Version: usbip.Version, Command: usbip.OpReqImport}).Write(conn); err != nil {
		t.Fatal(err)
	}
	var busID [32]byte
	copy(busID[:], "1-1")
	if _, err := conn.Write(busID[:]); err != nil {
		t.Fatal(err)
	}
	var reply [8 + 312]byte
	if err := usbip.ReadExactly(conn, reply[:]); err != nil {
		t.Fatal(err)
	}
	if status := binary.BigEndian.Uint32(reply[4:8]); status != 0 {
		t.Fatalf("import status %d", status)
	}
	return &deckStream{t: t, conn: conn, deck: deck, nextIn: 1}
}

// submitIn sends one interrupt-IN URB and returns its sequence number.
func (s *deckStream) submitIn(ep uint32) uint32 {
	s.t.Helper()
	seq := s.nextIn
	s.nextIn++
	cmd := usbip.CmdSubmit{
		Basic:             usbip.HeaderBasic{Command: usbip.CmdSubmitCode, Seqnum: seq, Dir: usbip.DirIn, Ep: ep},
		TransferBufferLen: 64,
		Interval:          6,
	}
	if err := cmd.Write(s.conn); err != nil {
		s.t.Fatal(err)
	}
	return seq
}

func (s *deckStream) unlink(target uint32) {
	s.t.Helper()
	seq := s.nextIn
	s.nextIn++
	cmd := usbip.CmdUnlink{
		Basic:        usbip.HeaderBasic{Command: usbip.CmdUnlinkCode, Seqnum: seq, Dir: usbip.DirOut, Ep: 0},
		UnlinkSeqnum: target,
	}
	if err := cmd.Write(s.conn); err != nil {
		s.t.Fatal(err)
	}
}

type reply struct {
	command uint32
	seq     uint32
	status  int32
	payload []byte
}

// read reads one reply, failing the test if none arrives within timeout. ok is false when the
// deadline passed with nothing on the wire.
func (s *deckStream) read(timeout time.Duration) (reply, bool) {
	s.t.Helper()
	_ = s.conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = s.conn.SetReadDeadline(time.Time{}) }()
	var header [usbip.HeaderSize]byte
	if err := usbip.ReadExactly(s.conn, header[:]); err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return reply{}, false
		}
		s.t.Fatal(err)
	}
	out := reply{
		command: binary.BigEndian.Uint32(header[0:4]),
		seq:     binary.BigEndian.Uint32(header[4:8]),
		status:  int32(binary.BigEndian.Uint32(header[20:24])),
	}
	if out.command == usbip.RetSubmitCode {
		length := binary.BigEndian.Uint32(header[24:28])
		out.payload = make([]byte, length)
		if length > 0 {
			if err := usbip.ReadExactly(s.conn, out.payload); err != nil {
				s.t.Fatal(err)
			}
		}
	}
	return out, true
}

// A URB on an endpoint that stays pending must be completable by UNLINK alone, and must not
// produce a completion afterwards. The Steam Deck's keyboard endpoint is the placeholder case:
// it has no input of its own and NAKs when idle, so nothing but an unlink ends its URB.
func TestUnlinkReleasesAPendingInterruptInURB(t *testing.T) {
	stream := newDeckStream(t, 64*time.Millisecond)
	target := stream.submitIn(deckKeyboardEndpoint)

	if _, ok := stream.read(50 * time.Millisecond); ok {
		t.Fatal("placeholder endpoint completed a URB while idle")
	}

	stream.unlink(target)
	got, ok := stream.read(2 * time.Second)
	if !ok {
		t.Fatal("no reply to USBIP_CMD_UNLINK")
	}
	if got.command != usbip.RetUnlinkCode {
		t.Fatalf("reply command = %#x, want RET_UNLINK", got.command)
	}
	// -ECONNRESET: the URB was unlinked before it completed.
	if got.status != -104 {
		t.Fatalf("unlink status = %d, want -104", got.status)
	}
	if _, ok := stream.read(50 * time.Millisecond); ok {
		t.Fatal("an unlinked URB was completed anyway")
	}
}

// Unlinking a URB that the endpoint is actively waiting on must release it without waiting out
// the endpoint's idle interval, and must not complete it.
func TestUnlinkWakesAWaitingEndpoint(t *testing.T) {
	stream := newDeckStream(t, time.Hour)
	// Take the endpoint past its first completion, so the next wait is the idle one.
	stream.submitIn(deckControllerEndpoint)
	if _, ok := stream.read(2 * time.Second); !ok {
		t.Fatal("the controller endpoint did not complete its first URB")
	}
	target := stream.submitIn(deckControllerEndpoint)

	start := time.Now()
	stream.unlink(target)
	got, ok := stream.read(2 * time.Second)
	if !ok {
		t.Fatal("no reply to USBIP_CMD_UNLINK")
	}
	if got.command != usbip.RetUnlinkCode || got.status != -104 {
		t.Fatalf("reply command = %#x status = %d, want RET_UNLINK and -104", got.command, got.status)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("unlink took %v, so it waited for the idle interval", elapsed)
	}
	if _, ok := stream.read(50 * time.Millisecond); ok {
		t.Fatal("an unlinked URB was completed anyway")
	}
}

// An endpoint that has already repeated itself once waits the idle interval before repeating
// again, instead of resending the same report every bInterval.
func TestAnIdleEndpointRepeatsAtTheIdleInterval(t *testing.T) {
	const idle = 150 * time.Millisecond
	stream := newDeckStream(t, idle)

	// The first URB is answered with the current state, the second is the first repeat and still
	// arrives at the endpoint's bInterval.
	for i := range 2 {
		stream.submitIn(deckControllerEndpoint)
		if _, ok := stream.read(2 * time.Second); !ok {
			t.Fatalf("URB %d did not complete", i)
		}
	}

	stream.submitIn(deckControllerEndpoint)
	start := time.Now()
	if _, ok := stream.read(2 * time.Second); !ok {
		t.Fatal("the idle repeat never arrived")
	}
	elapsed := time.Since(start)
	if elapsed < idle/2 {
		t.Fatalf("idle repeat arrived after %v, so it still repeats at the bInterval", elapsed)
	}
}

// Fresh input is never held by the idle interval: it completes at the endpoint's poll cadence,
// which is what makes a long idle interval free of input latency.
func TestFreshInputIsNotHeldByTheIdleInterval(t *testing.T) {
	stream := newDeckStream(t, time.Hour)
	// Get the endpoint past its first completion and into the idle wait.
	stream.submitIn(deckControllerEndpoint)
	if _, ok := stream.read(2 * time.Second); !ok {
		t.Fatal("the controller endpoint did not complete its first URB")
	}
	stream.submitIn(deckControllerEndpoint)

	var state steamdeck.InputState
	state.LStickX = 1234
	start := time.Now()
	stream.deck.UpdateInputState(&state)

	got, ok := stream.read(2 * time.Second)
	if !ok {
		t.Fatal("fresh input did not complete the pending URB")
	}
	if elapsed := time.Since(start); elapsed > 20*deckInterval {
		t.Fatalf("fresh input took %v to reach the host", elapsed)
	}
	if got.command != usbip.RetSubmitCode {
		t.Fatalf("reply command = %#x, want RET_SUBMIT", got.command)
	}
	if len(got.payload) < 50 {
		t.Fatalf("report is %d bytes", len(got.payload))
	}
	if x := int16(binary.LittleEndian.Uint16(got.payload[48:50])); x != 1234 {
		t.Fatalf("left stick X = %d, want 1234", x)
	}
}

// A polled endpoint reports on its bInterval grid, not on the consumer's sample cadence, and
// numbers every report it sends. With input every 8 ms on the Deck's 6 ms endpoint the worker
// used to complete the moment a sample landed, so the host saw a held report and a fresh one a
// fraction of a millisecond apart every few samples, the held one carrying the packet number it
// had already seen. Against live Steam that was a gyro microstutter (2026-09-26).
func TestCompletionsStayOnTheEndpointGrid(t *testing.T) {
	stream := newDeckStream(t, 0)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var state steamdeck.InputState
		ticker := time.NewTicker(8 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				state.LStickX++
				stream.deck.UpdateInputState(&state)
			case <-stop:
				return
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()

	var last time.Time
	var lastPacket uint32
	for i := range 60 {
		stream.submitIn(deckControllerEndpoint)
		got, ok := stream.read(2 * time.Second)
		if !ok {
			t.Fatalf("URB %d did not complete", i)
		}
		now := time.Now()
		packet := binary.LittleEndian.Uint32(got.payload[4:8])
		if i > 0 {
			if gap := now.Sub(last); gap < deckInterval/2 {
				t.Fatalf("report %d arrived %v after the previous one, so the endpoint followed its input instead of its bInterval", i, gap)
			}
			if packet != lastPacket+1 {
				t.Fatalf("report %d carries packet number %d after %d; the counter moves once per report", i, packet, lastPacket)
			}
		}
		last, lastPacket = now, packet
	}
}
