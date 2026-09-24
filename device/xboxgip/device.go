package xboxgip

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Alia5/VIIPER/device"
	"github.com/Alia5/VIIPER/usb"
	"github.com/Alia5/VIIPER/usbip"
)

// XboxGIP implements a GIP (Game Input Protocol) Xbox Series X|S controller.
type XboxGIP struct {
	// inputState is stored by value: retaining a caller pointer forced a
	// heap allocation per UpdateInputState call at input rate.
	inputState InputState
	stateMu    sync.Mutex
	outputFunc func(OutputState)
	descriptor usb.Descriptor

	// GIP protocol state. Metadata is served from the response queue with
	// priority over Hello/status/input frames.
	gipState  int32  // atomic: stateArrival / stateActive
	seqDevice uint32 // atomic: device-side sequence counter (1-255)

	// Response queue for EP IN (metadata fragments, acks, status).
	// These are returned with priority over Hello/input messages.
	respQueue chan []byte

	// Response queue for EP2 IN (auth channel). Fake-auth replies and
	// any future bidirectional secondary-interface traffic.
	respQueueAuth chan []byte

	// Hello cadence — per MS-GIPUSB §2.1.1.2 the device must emit Hello at
	// arrival and keep emitting it every 500 ms until the host responds
	// with a metadata-request (0x04) or SetState (0x05). We must NEVER
	// return successful zero-length EP1 IN completions in the interim
	// (Windows will treat the device as misbehaving), so handleEPIn
	// synchronously blocks the URB until the next 500 ms tick.
	helloMu     sync.Mutex
	lastHelloAt time.Time

	// 2269+ wedge: assume-active watchdog. If the host hasn't sent any
	// EP1/EP2 OUT within assumeActiveDelayMs AFTER the device's first Hello,
	// transition out of Arrival on the assumption that Windows is silently
	// consuming our Hello from cached metadata and never re-issues 0x04/0x05
	// (spec §2.1.1.4 "vendor messages MAY be sent before START if START can
	// be safely assumed"). Measured from firstHelloAt (NOT device creation)
	// so we actually send a few Hellos before assuming-active — Windows
	// almost certainly needs to see Hello to seed kernel-side state.
	// hostResponded latches 1 on first EP OUT received so the watchdog
	// disengages once Windows is actually talking to us.
	firstHelloAt      time.Time // protected by helloMu; set on first Hello emit
	assumeActiveDelay time.Duration
	hostResponded     int32 // atomic: 0 until first EP1/EP2 OUT arrives

	// Experimental Arrival-state status pulse. Windows' xboxgip.sys promotes
	// a slot to GipConn_Interrogating from its 0x03 status handler; this stays
	// disabled by default because the public GIPUSB flow says Hello-only until
	// host response.
	arrivalStatusInterval time.Duration
	lastArrivalStatusAt   time.Time // protected by helloMu
	lastIdleStatusAt      time.Time // protected by helloMu

	// Debug log file
	debugLog    *os.File
	loggedFirst int32 // atomic: set to 1 after first input report is logged

	// Guide button edge tracking. We emit a separate GIPVirtualKey (0x07)
	// packet on every press/release transition so SDL HIDAPI's
	// HandleModePacket fires SDL_GAMEPAD_BUTTON_GUIDE.
	prevGuideDown int32 // atomic: 0 = up, 1 = down

	// Paddle byte edge tracking. Elite 2 firmware 5.13+ sends an
	// additional 0x0C UNMAPPED_STATE packet on every paddle change. Some
	// driver code paths read paddle state from there instead of (or in
	// addition to) the inline byte in the 0x20 input report.
	prevPaddles int32 // atomic: low 8 bits = previous paddle byte
}

type xboxGIPCreateOptions struct{}

// msOSVendorCode is used in the MS OS String Descriptor and as bRequest
// for Extended Compatible ID queries.
const msOSVendorCode = 0x90

// New returns a new XboxGIP device.
func New(o *device.CreateOptions) (*XboxGIP, error) {
	d := &XboxGIP{
		descriptor:            makeDescriptor(),
		respQueue:             make(chan []byte, 32),
		respQueueAuth:         make(chan []byte, 8),
		assumeActiveDelay:     configuredAssumeActiveDelay(),
		arrivalStatusInterval: configuredArrivalStatusInterval(),
	}
	atomic.StoreUint32(&d.seqDevice, 1)
	// Start in Arrival state — xboxgip.sys expects Hello (0x02) first,
	// then drives the Metadata/SetState handshake before accepting input reports.
	atomic.StoreInt32(&d.gipState, stateArrival)

	// Open debug log file next to the executable.
	if exePath, err := os.Executable(); err == nil {
		logPath := filepath.Join(filepath.Dir(exePath), "xboxgip_debug.log")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			d.debugLog = f
			d.logf("GIP device created")
		}
	}

	if o != nil {
		if o.IDVendor != nil {
			d.descriptor.Device.IDVendor = *o.IDVendor
		}
		if o.IDProduct != nil {
			d.descriptor.Device.IDProduct = *o.IDProduct
		}
		if o.DeviceSpecific != "" {
			data := []byte(o.DeviceSpecific)
			var args xboxGIPCreateOptions
			if err := json.Unmarshal(data, &args); err != nil {
				return nil, fmt.Errorf("invalid JSON payload: %w", err)
			}
		}
	}

	d.logf("VID=0x%04X PID=0x%04X", d.descriptor.Device.IDVendor, d.descriptor.Device.IDProduct)
	d.logf("assume-active delay=%dms (0 disables watchdog)", d.assumeActiveDelay.Milliseconds())
	d.logf("arrival-status interval=%dms (0 disables experimental pulse)", d.arrivalStatusInterval.Milliseconds())
	return d, nil
}

func configuredAssumeActiveDelay() time.Duration {
	if raw := os.Getenv("VIIPER_GIP_ASSUME_ACTIVE_MS"); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err == nil {
			if ms <= 0 {
				return 0
			}
			return time.Duration(ms) * time.Millisecond
		}
	}
	return assumeActiveDelayMs * time.Millisecond
}

func configuredArrivalStatusInterval() time.Duration {
	if raw := os.Getenv("VIIPER_GIP_ARRIVAL_STATUS_MS"); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err == nil {
			if ms <= 0 {
				return 0
			}
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 0
}

// logf writes a timestamped line to the debug log file.
func (d *XboxGIP) logf(format string, args ...any) {
	if d.debugLog == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("15:04:05.000")
	fmt.Fprintf(d.debugLog, "%s  %s\n", ts, msg)
	d.debugLog.Sync()
}

// nextSeq returns the next GIP sequence number (1-255 wrapping).
func (d *XboxGIP) nextSeq() uint8 {
	for {
		old := atomic.LoadUint32(&d.seqDevice)
		next := old + 1
		if next > 255 {
			next = 1
		}
		if atomic.CompareAndSwapUint32(&d.seqDevice, old, next) {
			return uint8(next)
		}
	}
}

// SetOutputCallback sets the callback for force-feedback data (rumble).
func (d *XboxGIP) SetOutputCallback(f func(OutputState)) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	d.outputFunc = f
}

// UpdateInputState updates the current controller state (thread-safe).
func (d *XboxGIP) UpdateInputState(state *InputState) {
	d.stateMu.Lock()
	if state == nil {
		d.inputState = InputState{}
	} else {
		d.inputState = *state
	}
	d.stateMu.Unlock()
}

// HandleTransfer implements the GIP protocol over interrupt IN/OUT endpoints.
func (d *XboxGIP) HandleTransfer(ctx context.Context, ep uint32, dir uint32, out []byte) []byte {
	d.logf("HandleTransfer ep=%d dir=%d outLen=%d", ep, dir, len(out))
	if dir == usbip.DirIn && ep == 1 {
		if device.GateCancelled == device.BlockUntilDeadline(ctx) {
			return nil
		}
		return d.handleEPIn()
	}
	if dir == usbip.DirOut && ep == 1 {
		d.handleEPOut(out)
	}
	// EP2 OUT (interface 1 — GIP secondary / auth channel). Real Elite 2
	// receives auth challenges and metadata-related traffic here. Without
	// responding, xboxgip.sys appears to keep the device in a limited
	// state where Guide button / paddles aren't surfaced to apps.
	if dir == usbip.DirOut && ep == 2 {
		d.handleEP2Out(out)
		return nil
	}
	// EP2 IN: serve any queued auth responses, else NAK with a single zero
	// byte so USBIP doesn't return an empty response.
	if dir == usbip.DirIn && ep == 2 {
		if device.GateCancelled == device.BlockUntilDeadline(ctx) {
			return nil
		}
		select {
		case resp := <-d.respQueueAuth:
			d.logf("EP2 IN: queued auth resp len=%d hex=%X", len(resp), resp)
			return resp
		default:
		}
		return []byte{0x00}
	}
	// EP3 (interface 2 alt 1 — bulk expansion channel). Real wired Xbox
	// One controllers expose this for chatpad data (older bodies) or the
	// Microsoft auth challenge/response (Elite Series 2). We can't sign
	// the auth challenge without Microsoft's keys, and we don't emulate
	// a chatpad, so the host just sees empty bulk traffic. The point of
	// the interface being present is to match real-controller descriptor
	// shape — xboxgip.sys appears to gate IGamepad publication on the
	// full 3-interface configuration showing up at enumeration time.
	if dir == usbip.DirOut && ep == 3 {
		d.logf("EP3 OUT (iface 2 bulk): len=%d hex=%X", len(out), out)
		return nil
	}
	if dir == usbip.DirIn && ep == 3 {
		if device.GateCancelled == device.BlockUntilDeadline(ctx) {
			return nil
		}
		// Silent NAK. Real chatpad/auth would return signed bytes here.
		return []byte{0x00}
	}
	return nil
}

// handleEP2Out processes host → device traffic on the GIP secondary
// interface. Empirical observation (xboxgip_debug.log on real hardware
// + Windows xboxgip.sys): the host sends only LED color commands
// (type=0x0E) on EP2 OUT, not auth challenges. We log everything and
// only queue a fake-auth response if we actually see a 0x06 packet.
func (d *XboxGIP) handleEP2Out(data []byte) {
	if len(data) == 0 {
		return
	}
	// 2269 wedge: latch the host-responded flag so the assume-active
	// watchdog in handleEPIn disengages. Even non-protocol-protocol OUT
	// traffic (LED, vendor) proves the host is actively driving us.
	atomic.StoreInt32(&d.hostResponded, 1)
	d.logf("EP2 OUT: len=%d hex=%X", len(data), data)
	if len(data) < 4 {
		return
	}
	msgType := data[0]
	flags := data[1]
	seq := data[2]
	stateBefore := atomic.LoadInt32(&d.gipState)
	d.logf("EP2 OUT: type=0x%02X flags=0x%02X seq=%d state=%d", msgType, flags, seq, stateBefore)

	// 0x0E on EP2 is the Elite-2-specific command we saw the host send in
	// earlier 0x0B00 runs. It is not SetState START, so keep protocol state
	// unchanged and wait for the real metadata/SetState handshake.
	if msgType == 0x0E {
		d.logf("EP2 OUT: RX 0x0E payload=%X — not a SetState; staying in state=%d",
			data[4:], stateBefore)
	}

	if msgType == GIPAuthenticate {
		authResp := []byte{
			GIPAuthenticate, // 0x06
			GIPFlagSystem,   // 0x20
			d.nextSeq(),
			0x02, // payload length = 2
			0x01, // "security ok"
			0x00,
		}
		select {
		case d.respQueueAuth <- authResp:
			d.logf("EP2 OUT: queued fake-auth security-passed on EP2 IN")
		default:
			d.logf("EP2 OUT: WARNING auth response queue full")
		}
	}
}

// handleEPIn returns data for the host.
// Priority: Guide-button edge (0x07) > queued responses > Hello (Arrival) /
// input (Active). NEVER returns nil — always sends data to avoid 0-byte
// USBIP responses.
func (d *XboxGIP) handleEPIn() []byte {
	state := atomic.LoadInt32(&d.gipState)

	// 2269 wedge: assume-active watchdog. Measured from FIRST Hello sent
	// (not device creation), so we always emit a few Hellos to seed the
	// host's kernel-side state before assuming-active. If the host hasn't
	// sent any EP1/EP2 OUT within assumeActiveDelayMs of the first Hello,
	// queue Status + transition to Active.
	if state == stateArrival && d.assumeActiveDelay > 0 && atomic.LoadInt32(&d.hostResponded) == 0 {
		d.helloMu.Lock()
		fha := d.firstHelloAt
		d.helloMu.Unlock()
		if !fha.IsZero() && time.Since(fha) >= d.assumeActiveDelay {
			d.logf("EP IN: assume-active watchdog fired (%dms since first Hello, host silent on EP OUT) — transitioning Arrival→Active",
				d.assumeActiveDelay.Milliseconds())
			status := buildStatusMessage(d.nextSeq())
			select {
			case d.respQueue <- status:
				d.logf("GIP: queued Status (assume-active)")
			default:
			}
			atomic.StoreInt32(&d.gipState, stateActive)
			state = stateActive
		}
	}

	if state == stateArrival && d.arrivalStatusInterval > 0 && atomic.LoadInt32(&d.hostResponded) == 0 {
		now := time.Now()
		d.helloMu.Lock()
		fha := d.firstHelloAt
		lastStatus := d.lastArrivalStatusAt
		shouldSend := !fha.IsZero() &&
			now.Sub(fha) >= d.arrivalStatusInterval &&
			(lastStatus.IsZero() || now.Sub(lastStatus) >= d.arrivalStatusInterval)
		if shouldSend {
			d.lastArrivalStatusAt = now
		}
		d.helloMu.Unlock()

		if shouldSend {
			status := buildStatusMessage(d.nextSeq())
			d.logf("EP IN: Arrival-status experiment (seq=%d, interval=%dms)", status[2], d.arrivalStatusInterval.Milliseconds())
			return status
		}
	}

	// Guide button edge — emit a separate 0x07 VirtualKey packet so SDL's
	// HandleModePacket fires SDL_GAMEPAD_BUTTON_GUIDE. We only check this
	// in Active state; xinputGuide bits before the host has enabled input
	// would be wasted.
	if state == stateActive {
		d.stateMu.Lock()
		pre := d.inputState
		d.stateMu.Unlock()
		guideDown := (pre.Buttons & xinputGuide) != 0
		newVal := int32(0)
		if guideDown {
			newVal = 1
		}
		if old := atomic.LoadInt32(&d.prevGuideDown); old != newVal &&
			atomic.CompareAndSwapInt32(&d.prevGuideDown, old, newVal) {
			pkt := buildVirtualKeyPacket(d.nextSeq(), guideDown, gipVKeyLeftWin)
			d.logf("EP IN: Guide edge → 0x07 (down=%v)", guideDown)
			return pkt
		}

		// Paddle edge — emit 0x0C UNMAPPED_STATE so firmware-5.13+ driver
		// paths can pick up the paddle state from the canonical message
		// type. The inline byte 18/19 of the 0x20 input report remains too.
		paddleByte := int32(pre.Paddles())
		modeByte := pre.PaddleMode()
		if old := atomic.LoadInt32(&d.prevPaddles); old != paddleByte &&
			atomic.CompareAndSwapInt32(&d.prevPaddles, old, paddleByte) {
			pkt := buildUnmappedStatePacket(d.nextSeq(), byte(paddleByte), modeByte)
			d.logf("EP IN: Paddle edge → 0x0C (paddles=0x%02X mode=0x%02X)", byte(paddleByte), modeByte)
			return pkt
		}
	}

	// Check response queue (metadata fragments, acks, status).
	select {
	case resp := <-d.respQueue:
		d.logf("EP IN: queued response len=%d type=0x%02X", len(resp), resp[0])
		return resp
	default:
	}

	switch state {
	case stateActive:
		// Send current input report.
		d.stateMu.Lock()
		st := d.inputState
		d.stateMu.Unlock()

		// Elite 2 wired (PID 0x0B00) → 46-byte firmware-5.x layout with
		// paddle byte at off 18 + mode byte at off 19. Everything else
		// → standard 18-byte input report.
		var rpt []byte
		if d.descriptor.Device.IDProduct == 0x0B00 {
			rpt = buildInputReportElite2(&st, d.nextSeq(), st.Paddles(), st.PaddleMode())
		} else {
			rpt = buildInputReport(&st, d.nextSeq())
		}
		if atomic.CompareAndSwapInt32(&d.loggedFirst, 0, 1) {
			d.logf("EP IN: first input report size=%d hex=%X buttons=0x%08X LT=%d RT=%d LX=%d LY=%d RX=%d RY=%d paddles=0x%02X mode=0x%02X",
				len(rpt), rpt, st.Buttons, st.LT, st.RT, st.LX, st.LY, st.RX, st.RY, st.Paddles(), st.PaddleMode())
		}
		// Log any frame where paddles or share fire so we can see what the
		// device is actually emitting when the user presses them.
		if st.Paddles() != 0 || (st.Buttons&xinputGuide) != 0 {
			d.logf("EP IN: input report size=%d buttons=0x%08X paddles=0x%02X mode=0x%02X hex=%X",
				len(rpt), st.Buttons, st.Paddles(), st.PaddleMode(), rpt)
		}
		return rpt

	case stateIdle:
		// Idle follows metadata when the host sends SetState STOP, which is
		// common for headset-capable Xbox controllers. Keep the connection
		// alive with status reports, but do not restart Hello; another Hello
		// makes xboxgip.sys begin metadata enumeration again.
		d.helloMu.Lock()
		var sleepDur time.Duration
		if !d.lastIdleStatusAt.IsZero() {
			elapsed := time.Since(d.lastIdleStatusAt)
			if elapsed < helloIntervalMs*time.Millisecond {
				sleepDur = helloIntervalMs*time.Millisecond - elapsed
			}
		}
		d.helloMu.Unlock()
		if sleepDur > 0 {
			time.Sleep(sleepDur)
		}
		d.helloMu.Lock()
		d.lastIdleStatusAt = time.Now()
		d.helloMu.Unlock()

		status := buildStatusMessage(d.nextSeq())
		d.logf("EP IN: Status (Idle, seq=%d)", status[2])
		return status

	default:
		// Arrival state — proper MS-GIPUSB §2.1.1.2 cadence: emit Hello
		// every 500 ms until the host responds with a metadata-request
		// (0x04) or SetState (0x05) on EP1 OUT. Do NOT pre-queue metadata
		// or input reports — the host drives the rest of the handshake.
		//
		// Previously this code force-transitioned to Active on the first
		// poll and pushed metadata/status/firmware/input-reports unsolicited,
		// which violated the protocol invariant "device speaks Hello first,
		// host responds". Windows treated those unsolicited frames as
		// invalid and never advanced to the metadata-request stage, so the
		// IGamepad PDO was never published.
		//
		// We synchronously block this URB until the next 500 ms tick is due,
		// rather than completing with actual_length=0 (which Windows would
		// see as a flood of successful zero-length packets — exactly the
		// "spinning empty completions" anti-pattern flagged in the GIPUSB
		// spec). The libviiper USBIP server processes URBs serially, so the
		// blocked goroutine holds this URB pending at the wire level.
		d.helloMu.Lock()
		var sleepDur time.Duration
		if !d.lastHelloAt.IsZero() {
			elapsed := time.Since(d.lastHelloAt)
			if elapsed < helloIntervalMs*time.Millisecond {
				sleepDur = helloIntervalMs*time.Millisecond - elapsed
			}
		}
		d.helloMu.Unlock()
		if sleepDur > 0 {
			time.Sleep(sleepDur)
		}
		d.helloMu.Lock()
		now := time.Now()
		d.lastHelloAt = now
		if d.firstHelloAt.IsZero() {
			d.firstHelloAt = now
		}
		d.helloMu.Unlock()

		vid := d.descriptor.Device.IDVendor
		pid := d.descriptor.Device.IDProduct
		seq := d.nextSeq()
		hello := buildHelloMessage(seq, vid, pid)
		d.logf("EP IN: Hello (Arrival, seq=%d, vid=0x%04X, pid=0x%04X)", seq, vid, pid)
		return hello
	}
}

// handleEPOut processes GIP commands from the host on EP OUT.
func (d *XboxGIP) handleEPOut(data []byte) {
	if len(data) < 4 {
		return
	}

	msgType := data[0]
	flags := data[1]
	seq := data[2]
	stateBefore := atomic.LoadInt32(&d.gipState)

	// 2269 wedge: latch the host-responded flag so the assume-active
	// watchdog in handleEPIn disengages. Any EP1 OUT message — even rumble
	// or LED — proves the host is talking to us.
	atomic.StoreInt32(&d.hostResponded, 1)

	d.logf("EP OUT: type=0x%02X flags=0x%02X seq=%d len=%d state=%d data=%X",
		msgType, flags, seq, len(data), stateBefore, data)

	switch msgType {
	case GIPDescriptor: // 0x04 — Metadata Request
		d.handleMetadataRequest(seq)

	case GIPSetState: // 0x05 — Set Device State
		if len(data) >= 5 {
			d.handleSetState(data[4], seq)
		}

	case GIPAuthenticate: // 0x06 — Auth
		d.logf("GIP: ignoring auth message seq=%d", seq)

	case GIPRumble: // 0x09 — Force Feedback
		d.handleRumble(data)

	case GIPLedControl: // 0x0A — LED
		d.logf("GIP: ignoring LED control seq=%d", seq)

	case GIPAcknowledge: // 0x01 — ACK from host
		d.logf("GIP: received ACK seq=%d", seq)

	case 0x0E: // Elite-specific / vendor / upper-stack ping
		// 0x0E is observed on Elite-2 PID 0x0B00 during Arrival, but it is
		// not a SetState START. Treating it as a publish trigger caused us to
		// send status/input before the host had requested metadata.
		d.logf("EP OUT: RX 0x0E payload=%X — not a SetState; staying in state=%d",
			data[4:], stateBefore)

	default:
		d.logf("GIP: unhandled type=0x%02X flags=0x%02X seq=%d", msgType, flags, seq)
	}
}

// handleMetadataRequest responds to a metadata request by queueing
// the fragmented metadata blob and a device status message.
func (d *XboxGIP) handleMetadataRequest(hostSeq uint8) {
	d.logf("GIP: metadata request received (hostSeq=%d), queueing metadata", hostSeq)
	slog.Info("GIP: metadata request received, sending metadata")

	if drained := d.drainResponseQueue(); drained > 0 {
		d.logf("GIP: drained %d stale queued response(s) before metadata retry", drained)
	}

	// Fragment the metadata blob.
	seq := d.nextSeq()
	fragments := fragmentMetadata(seq)
	totalMetaLen := len(gipMetadata)
	for i, frag := range fragments {
		select {
		case d.respQueue <- frag:
			d.logf("GIP: queued metadata fragment %d/%d len=%d", i+1, len(fragments), len(frag))
		default:
			d.logf("GIP: WARNING response queue full, dropping fragment %d", i+1)
			slog.Warn("GIP: response queue full, dropping metadata fragment")
		}
	}

	// Queue the Metadata Complete sentinel (zero-payload 0x04 A0 <seq> 00 ...)
	// using the SAME sequence as the data fragments. Per MS-GIPUSB §2.1.1.3
	// the host needs this to close out the metadata transaction; without it
	// xboxgip.sys considers the transfer incomplete and never advances.
	complete := buildMetadataComplete(seq, totalMetaLen)
	select {
	case d.respQueue <- complete:
		d.logf("GIP: queued metadata complete (seq=%d, totalLen=%d)", seq, totalMetaLen)
	default:
		d.logf("GIP: WARNING response queue full, dropping metadata complete")
	}

	// Don't queue status here — wait for SetState Start from the host (spec
	// order: metadata → SetState Start → status → input). Status is sent
	// from handleSetState's GIPStateStart branch instead.
}

func (d *XboxGIP) drainResponseQueue() int {
	drained := 0
	for {
		select {
		case <-d.respQueue:
			drained++
		default:
			return drained
		}
	}
}

// handleSetState processes power state changes from the host.
func (d *XboxGIP) handleSetState(stateCode byte, seq uint8) {
	d.logf("GIP: SetState code=0x%02X seq=%d", stateCode, seq)

	switch stateCode {
	case GIPStateStart: // 0x00 → Active
		d.logf("GIP: → Active (input reports enabled)")
		slog.Info("GIP: SetState START → Active")
		// Queue an initial status (wired, full power) before the first
		// input report. Per MS-GIPUSB §2.1.1.4 the status precedes the
		// first 0x20 input frame after the host issues SetState Start.
		status := buildStatusMessage(d.nextSeq())
		select {
		case d.respQueue <- status:
			d.logf("GIP: queued status on SetState Start")
		default:
		}
		atomic.StoreInt32(&d.gipState, stateActive)

	case GIPStateStop: // 0x01 → Idle
		d.logf("GIP: → Idle (input reports paused)")
		slog.Info("GIP: SetState STOP → Idle")
		atomic.StoreInt32(&d.gipState, stateIdle)
		d.helloMu.Lock()
		d.lastIdleStatusAt = time.Time{}
		d.helloMu.Unlock()

	case GIPStateOff: // 0x04 → Off
		d.logf("GIP: → Off (power down)")
		slog.Info("GIP: SetState OFF")
		status := buildStatusMessage(d.nextSeq())
		select {
		case d.respQueue <- status:
		default:
		}
		atomic.StoreInt32(&d.gipState, stateArrival)
		d.helloMu.Lock()
		d.lastIdleStatusAt = time.Time{}
		d.helloMu.Unlock()

	case GIPStateQuiesce: // 0x05 — Clear motors
		d.logf("GIP: Quiesce (clearing motors)")
		d.stateMu.Lock()
		fn := d.outputFunc
		d.stateMu.Unlock()
		if fn != nil {
			fn(OutputState{})
		}

	case GIPStateReset: // 0x07 — Full reset
		d.logf("GIP: → Reset (restarting)")
		slog.Info("GIP: SetState RESET → Arrival")
		atomic.StoreInt32(&d.gipState, stateArrival)
		atomic.StoreUint32(&d.seqDevice, 1)
		d.helloMu.Lock()
		d.lastHelloAt = time.Time{}
		d.firstHelloAt = time.Time{}
		d.lastArrivalStatusAt = time.Time{}
		d.lastIdleStatusAt = time.Time{}
		d.helloMu.Unlock()
		atomic.StoreInt32(&d.hostResponded, 0)

	default:
		d.logf("GIP: unknown state code 0x%02X", stateCode)
	}
}

// handleRumble parses a GIP force feedback message (command 0x09).
func (d *XboxGIP) handleRumble(data []byte) {
	if len(data) < 13 {
		return
	}
	leftTrigger := data[6]
	rightTrigger := data[7]
	leftMotor := data[8]
	rightMotor := data[9]

	d.stateMu.Lock()
	fn := d.outputFunc
	d.stateMu.Unlock()

	if fn != nil {
		fn(OutputState{
			LeftMotor:    leftMotor,
			RightMotor:   rightMotor,
			LeftTrigger:  leftTrigger,
			RightTrigger: rightTrigger,
		})
	}
}

// HandleControl handles EP0 control transfers for MS OS descriptors.
func (d *XboxGIP) HandleControl(bmRequestType, bRequest uint8, wValue, wIndex, wLength uint16, data []byte) ([]byte, bool) {
	d.logf("EP0: bmReqType=0x%02X bReq=0x%02X wVal=0x%04X wIdx=0x%04X wLen=%d",
		bmRequestType, bRequest, wValue, wIndex, wLength)

	// Standard GET_STATUS(Device) — return 2 bytes: not self-powered, no remote wakeup.
	if bmRequestType == 0x80 && bRequest == 0x00 && wIndex == 0x0000 {
		d.logf("EP0: returning GET_STATUS (device)")
		return []byte{0x00, 0x00}, true
	}

	// Vendor request: Extended Compatible ID OS Descriptor.
	// Accept ANY vendor code — Windows caches the code per VID:PID, so the real
	// Xbox controller's cached code (0xFD) may differ from our string descriptor (0x90).
	if bmRequestType == 0xC0 && wIndex == 0x0004 {
		d.logf("EP0: returning Extended Compatible ID (XGIP10)")
		resp := extCompatIDDescriptor[:]
		if int(wLength) < len(resp) {
			resp = resp[:wLength]
		}
		return resp, true
	}

	// MS OS Extended Properties (wIndex=0x0005) — return minimal empty descriptor
	// (just a 10-byte header with wCount=0). xboxgip.sys queries this for PID
	// 0x02D2 (Xbox One Controller). Returning nil/false caused CM_PROB_FAILED_START.
	if bmRequestType == 0xC0 && wIndex == 0x0005 {
		d.logf("EP0: returning empty Extended Properties (wIndex=5)")
		empty := []byte{
			0x0A, 0x00, 0x00, 0x00, // dwLength = 10
			0x00, 0x01, // bcdVersion = 1.0
			0x05, 0x00, // wIndex = 5
			0x00, 0x00, // wCount = 0
		}
		if int(wLength) < len(empty) {
			empty = empty[:wLength]
		}
		return empty, true
	}

	// Same shape for wIndex=0x0006 — some xboxgip queries this too.
	if bmRequestType == 0xC0 && wIndex == 0x0006 {
		d.logf("EP0: returning empty descriptor (wIndex=6)")
		empty := []byte{
			0x0A, 0x00, 0x00, 0x00, // dwLength = 10
			0x00, 0x01, // bcdVersion = 1.0
			0x06, 0x00, // wIndex = 6
			0x00, 0x00, // wCount = 0
		}
		if int(wLength) < len(empty) {
			empty = empty[:wLength]
		}
		return empty, true
	}

	// bmReqType=0xC1 (interface, vendor) wIndex=0x0005 — per-interface props.
	if bmRequestType == 0xC1 && wIndex == 0x0005 {
		d.logf("EP0: returning empty per-interface Extended Properties")
		empty := []byte{
			0x0A, 0x00, 0x00, 0x00,
			0x00, 0x01,
			0x05, 0x00,
			0x00, 0x00,
		}
		if int(wLength) < len(empty) {
			empty = empty[:wLength]
		}
		return empty, true
	}

	return nil, false
}

// NaksWhenIdle reports that GIP input is event-driven on real hardware.
func (d *XboxGIP) NaksWhenIdle() bool { return true }

func (d *XboxGIP) GetDescriptor() *usb.Descriptor {
	return &d.descriptor
}

func (d *XboxGIP) GetDeviceSpecificArgs() map[string]any {
	return nil
}

// makeDescriptor builds the USB descriptor for a GIP Xbox Series controller.
func makeDescriptor() usb.Descriptor {
	return usb.Descriptor{
		Device: usb.DeviceDescriptor{
			BcdUSB:          0x0200,
			BDeviceClass:    0xFF,
			BDeviceSubClass: 0x47,
			BDeviceProtocol: 0xD0,
			BMaxPacketSize0: 0x40,
			IDVendor:        0x045E,
			// PID 0x0B00 = wired Xbox Elite Series 2.
			//
			// On regular desktop Windows (Kernel-ProductInfo=0x65 PRODUCT_CORE)
			// xboxgip.sys gates state-machine progression to state 8 (which
			// triggers IGamepad PDO creation) behind two paths:
			//   - State 7 → 8: requires DAT_14005597d != 0 which is set
			//     only when Kernel-ProductInfo == 0xc6 (a specialized OS
			//     SKU, e.g. Xbox System OS / HoloLens / GamingOS).
			//   - State 5/6 → 8 direct: requires uVar17 != 0 OR
			//     (VID=0x45e AND PID=0x2d2 AND DAT_140055dc0[slot]==0) OR
			//     DAT_140055dc0[slot]==2.
			// PID 0x2D2 hits the fast-path BUT xboxgip's built-in
			// metadata for 0x2D2 marks it as DFU (firmware-update bootstrap)
			// → device shows "Xbox One Controller DFU" with
			// CM_PROB_FAILED_START. So 0x2D2 is also a dead end.
			//
			// On non-specialized desktop Windows GIP simply can't publish
			// IGamepad for arbitrary controllers. This is by design.
			// PID 0x02D1 = Xbox One Controller (original wired, NOT the
			// DFU bootstrap 0x02D2). xinputhid.inf binds HID children
			// with hardware ID `HID\VID_045E&PID_02D1&IG_00`. The PC
			// path for these children: Windows.Gaming.Input.dll /
			// GameInput.dll send IOCTL 0x40001d10 to xboxgip, which
			// triggers FUN_140037ff0 → FUN_14003746c → FUN_14003e028
			// (the PDO creator) — bypassing the state-machine license
			// gate at FUN_1400273c0. This is the working PC path.
			// 2269 0x0B00 wedge: revert to the PID/bcdDevice combo that
			// previously coaxed real EP1 OUT traffic (rumble 0x09 + the
			// Elite-specific 0x0E command) out of Windows on this machine.
			// 0x02EE/0x2268 (fresh cache) was a clean negative result; we
			// now want to test whether the "assume-active" branch (skip
			// waiting for 0x04/0x05 after 1.5s of Hello-only) unlocks the
			// publication path that the cached 0x0B00 binding is partially
			// engaged with.
			IDProduct:          0x0B00,
			BcdDevice:          0x0114,
			IManufacturer:      0x01,
			IProduct:           0x02,
			ISerialNumber:      0x03,
			BNumConfigurations: 0x01,
			Speed:              2, // Full-speed (12 Mbps) — MS-GIPUSB spec §2.2.1 requires FS; MUST STALL device_qualifier
		},
		Interfaces: []usb.InterfaceConfig{
			// Interface 0: GIP data — gamepad input/output, paddles, profile
			// state. bInterfaceProtocol 0xD0 matches the MS-GIPUSB spec's
			// "GIP data" protocol byte.
			{
				Descriptor: usb.InterfaceDescriptor{
					BInterfaceNumber:   0x00,
					BAlternateSetting:  0x00,
					BNumEndpoints:      0x02,
					BInterfaceClass:    0xFF,
					BInterfaceSubClass: 0x47,
					BInterfaceProtocol: 0xD0,
					IInterface:         0x00,
				},
				Endpoints: []usb.EndpointDescriptor{
					{BEndpointAddress: 0x81, BMAttributes: 0x03, WMaxPacketSize: 0x0040, BInterval: 0x04},
					{BEndpointAddress: 0x01, BMAttributes: 0x03, WMaxPacketSize: 0x0040, BInterval: 0x04},
				},
			},
			// Interface 1 alt 0: bulk expansion channel, inactive shape.
			// Diagnostic build (TEST A): audio interface removed so the
			// host does NOT classify us as audio-capable. MS-GIPUSB audio
			// startup sequence is Hello → metadata → STOP → Audio Control
			// Config → device replies → START; without an Audio Format
			// declared in metadata, dc1-controller sends STOP and then
			// stalls indefinitely (verified in helper xboxgip_debug.log).
			// Stripping the audio interface forces the non-audio startup
			// path: Hello → metadata → START. If START fires, we know
			// audio negotiation was the blocker.
			{
				Descriptor: usb.InterfaceDescriptor{
					BInterfaceNumber:   0x01,
					BAlternateSetting:  0x00,
					BNumEndpoints:      0x00,
					BInterfaceClass:    0xFF,
					BInterfaceSubClass: 0x47,
					BInterfaceProtocol: 0xD0,
					IInterface:         0x00,
				},
			},
			// Interface 1 alt 1: bulk expansion endpoints. EP 0x03 OUT /
			// 0x83 IN — chatpad / auth-channel endpoint addresses (kept
			// from the prior 3-interface design at addresses 0x03/0x83
			// so HandleTransfer's existing EP3 routing still works).
			{
				Descriptor: usb.InterfaceDescriptor{
					BInterfaceNumber:   0x01,
					BAlternateSetting:  0x01,
					BNumEndpoints:      0x02,
					BInterfaceClass:    0xFF,
					BInterfaceSubClass: 0x47,
					BInterfaceProtocol: 0xD0,
					IInterface:         0x00,
				},
				Endpoints: []usb.EndpointDescriptor{
					{BEndpointAddress: 0x03, BMAttributes: 0x02, WMaxPacketSize: 0x0040, BInterval: 0x00},
					{BEndpointAddress: 0x83, BMAttributes: 0x02, WMaxPacketSize: 0x0040, BInterval: 0x00},
				},
			},
		},
		Strings: map[uint8]string{
			0: "Љ", // LangID: en-US (0x0409)
			1: "Microsoft",
			2: "Xbox Elite Wireless Controller Series 2",
			3: "0000FFFB56495052",
			// Use \u0090, not \x90. The USB string encoder iterates runes;
			// a raw 0x90 byte is invalid UTF-8 and becomes U+FFFD, causing
			// Windows to use bRequest=0xFD instead of the real MS OS vendor
			// request byte 0x90.
			0xEE: "MSFT100\u0090",
		},
	}
}

var (
	_ usb.Device        = (*XboxGIP)(nil)
	_ usb.ControlDevice = (*XboxGIP)(nil)
)

var _ = binary.LittleEndian
