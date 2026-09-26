package usb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Alia5/VIIPER/internal/log"
	"github.com/Alia5/VIIPER/usb"
	"github.com/Alia5/VIIPER/usbip"
	"github.com/Alia5/VIIPER/virtualbus"
)

type batchingWriter struct {
	mu           sync.Mutex
	w            *bufio.Writer
	flushEvery   time.Duration
	flushAtBytes int
	stopCh       chan struct{}
	closeOnce    sync.Once
	err          error
}

const (
	// avoid windows socket overhead while keeping latency very low.
	writeBatcherBufferSize   = 256 * 1024
	writeBatcherFlushAtBytes = 64 * 1024

	// maxInterruptPayload sizes each endpoint worker's reusable completion frame. It is one
	// allocation per endpoint for the life of the URB stream, so it is generous enough that no
	// HID report has to be split or re-allocated.
	maxInterruptPayload = 1024
)

func newBatchingWriter(dst io.Writer, bufSize int, flushEvery time.Duration, flushAtBytes int) *batchingWriter {
	if bufSize <= 0 {
		bufSize = writeBatcherBufferSize
	}
	if flushAtBytes < 0 {
		flushAtBytes = 0
	}
	if flushAtBytes > bufSize {
		flushAtBytes = bufSize
	}
	bw := &batchingWriter{
		w:            bufio.NewWriterSize(dst, bufSize),
		flushEvery:   flushEvery,
		flushAtBytes: flushAtBytes,
		stopCh:       make(chan struct{}),
	}
	if flushEvery > 0 {
		go bw.flushLoop()
	}
	return bw
}

func (b *batchingWriter) flushLoop() {
	defer recoverAndLog(slog.Default(), "write batcher")
	t := time.NewTicker(b.flushEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = b.Flush()
		case <-b.stopCh:
			return
		}
	}
}

func (b *batchingWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return 0, b.err
	}

	n, err := b.w.Write(p)
	if err != nil {
		b.err = err
		return n, err
	}
	if b.flushAtBytes > 0 && b.w.Buffered() >= b.flushAtBytes {
		if err := b.w.Flush(); err != nil {
			b.err = err
			return n, err
		}
	}
	return n, nil
}

func (b *batchingWriter) Flush() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	if err := b.w.Flush(); err != nil {
		b.err = err
		return err
	}
	return nil
}

func (b *batchingWriter) Close() error {
	b.closeOnce.Do(func() {
		close(b.stopCh)
	})
	return b.Flush()
}

const (
	// USB standard request codes
	usbReqGetStatus        = 0x00
	usbReqClearFeature     = 0x01
	usbReqSetFeature       = 0x03
	usbReqSetAddress       = 0x05
	usbReqGetDescriptor    = 0x06
	usbReqSetDescriptor    = 0x07
	usbReqGetConfiguration = 0x08
	usbReqSetConfiguration = 0x09

	// USB descriptor types
	usbDescTypeDevice        = 0x01
	usbDescTypeConfiguration = 0x02
	usbDescTypeString        = 0x03
	usbDescTypeHID           = 0x21
	usbDescTypeHIDReport     = 0x22

	// USB request types (bmRequestType)
	usbReqTypeStandardToDevice    = 0x00
	usbReqTypeStandardToInterface = 0x81
	usbReqTypeStandardFromDevice  = 0x80
	usbReqTypeMask                = 0x60
	usbReqTypeClass               = 0x20

	// USB interface classes
	usbInterfaceClassHID = 0x03

	// HID class requests (bRequest)
	hidReqGetReport   = 0x01
	hidReqGetIdle     = 0x02
	hidReqGetProtocol = 0x03
	hidReqSetReport   = 0x09
	hidReqSetIdle     = 0x0A
	hidReqSetProtocol = 0x0B

	// HID class request types (bmRequestType)
	hidReqTypeIn  = 0xA1
	hidReqTypeOut = 0x21

	// wIndex low-byte interface selector mask.
	usbIfaceIndexMask = 0x00FF

	// USB configuration values
	usbConfigValueDefault   = 1
	usbConfigAttrBusPowered = 0xA0 // Bus-powered + remote wakeup (MS-GIPUSB §2.2.3)
	usbConfigMaxPower100mA  = 50   // In units of 2mA

	// URB header field offsets
	urbHdrSize          = 0x30
	urbHdrOffsetCommand = 0x00
	urbHdrOffsetSeqnum  = 0x04
	urbHdrOffsetDevid   = 0x08
	urbHdrOffsetDir     = 0x0c
	urbHdrOffsetEp      = 0x10
	urbHdrOffsetUnlink  = 0x14
	urbHdrOffsetFlags   = 0x14
	urbHdrOffsetLength  = 0x18
	urbHdrOffsetSetup   = 0x28

	// Standard header peek size
	headerPeekSize = 8

	// BUSID buffer size for import
	busIDSize = 32

	// Error codes
	errConnReset = -104 // -ECONNRESET
)

type Server struct {
	config    *ServerConfig
	logger    *slog.Logger
	rawLogger log.RawLogger
	busses    map[uint32]*virtualbus.VirtualBus
	busesMu   sync.Mutex
	ready     chan struct{}
	readyOnce sync.Once
	readyErr  error
	ln        net.Listener
}

func New(config ServerConfig, logger *slog.Logger, rawLogger log.RawLogger) *Server {
	return &Server{
		config:    &config,
		logger:    logger,
		rawLogger: rawLogger,
		busses:    make(map[uint32]*virtualbus.VirtualBus),
		ready:     make(chan struct{}),
	}
}

// AddBus registers a bus with the server. If the bus number is already present,
// an error is returned.
func (s *Server) AddBus(bus *virtualbus.VirtualBus) error {
	s.busesMu.Lock()
	defer s.busesMu.Unlock()
	if bus == nil {
		return fmt.Errorf("bus is nil")
	}
	if _, ok := s.busses[bus.BusID()]; ok {
		return fmt.Errorf("bus %d already registered", bus.BusID())
	}
	s.busses[bus.BusID()] = bus
	return nil
}

// RemoveBus unregisters a bus from the server.
func (s *Server) RemoveBus(busID uint32) error {
	s.busesMu.Lock()
	bus, ok := s.busses[busID]
	if !ok {
		s.busesMu.Unlock()
		return fmt.Errorf("bus %d not found", busID)
	}

	devices := bus.Devices()
	s.busesMu.Unlock()

	if len(devices) > 0 {
		s.logger.Warn(fmt.Sprintf("Removing non-empty bus %d with %d device(s) attached; removing devices", busID, len(devices)))
		for _, dev := range devices {
			_ = bus.Remove(dev)
		}
	}

	s.busesMu.Lock()
	delete(s.busses, busID)
	s.busesMu.Unlock()

	return bus.Close()
}

// RemoveDeviceByID removes a device by busId and cancels its connections.
func (s *Server) RemoveDeviceByID(busID uint32, deviceID string) error {
	s.busesMu.Lock()
	bus, ok := s.busses[busID]
	s.busesMu.Unlock()

	if !ok {
		return fmt.Errorf("bus %d not found", busID)
	}
	err := bus.RemoveDeviceByID(deviceID)
	if err != nil {
		return err
	}

	if emptyCtx := bus.GetBusEmptyContext(); emptyCtx != nil {
		go func() {
			defer recoverAndLog(s.logger, "bus cleanup")
			slog.Debug("Started bus cleanup goroutine (RemoveDeviceByID)")
			select {
			case <-emptyCtx.Done():
				// Cancelled - a new device was added
				return
			case <-time.After(s.config.BusCleanupTimeout):
				if b := s.GetBus(busID); b != nil && len(b.Devices()) == 0 {
					if err := s.RemoveBus(busID); err != nil {
						s.logger.Error("timeout: failed to remove empty bus", "busID", busID, "error", err)
					} else {
						s.logger.Info("timeout: removed empty bus", "busID", busID)
					}
				}
			}
		}()
	} else {
		s.logger.Debug("No bus empty context; Cleaning bus immediately")
		if b := s.GetBus(busID); b != nil && len(b.Devices()) == 0 {
			if err := s.RemoveBus(busID); err != nil {
				s.logger.Error("timeout: failed to remove empty bus", "busID", busID, "error", err)
			} else {
				s.logger.Info("timeout: removed empty bus", "busID", busID)
			}
		}
	}

	return nil
}

// ListBuses returns a snapshot of active bus numbers.
func (s *Server) ListBuses() []uint32 {
	s.busesMu.Lock()
	defer s.busesMu.Unlock()
	out := make([]uint32, 0, len(s.busses))
	for k := range s.busses {
		out = append(out, k)
	}
	return out
}

// GetBus returns a bus by ID or nil if not present.
func (s *Server) GetBus(busID uint32) *virtualbus.VirtualBus {
	s.busesMu.Lock()
	defer s.busesMu.Unlock()
	return s.busses[busID]
}

func (s *Server) NextFreeBusID() uint32 {
	s.busesMu.Lock()
	defer s.busesMu.Unlock()
	var id uint32 = 1
	for {
		if _, exists := s.busses[id]; !exists {
			return id
		}
		id++
	}
}

func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	if s.config != nil {
		return s.config.Addr
	}
	return ""
}

// ListenAndServe starts the USB-IP server and handles incoming connections.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.config.Addr)
	if err != nil {
		// Close ready here too: a caller waiting on Ready() to know the listener is bound
		// would otherwise block forever on a bind failure, since nothing else closes the
		// channel. readyErr is written before the close and read only after Ready()
		// unblocks a receiver, so the close is the happens-before edge for it.
		s.readyErr = err
		s.readyOnce.Do(func() { close(s.ready) })
		return err
	}
	s.ln = ln
	s.config.Addr = ln.Addr().String()
	s.readyOnce.Do(func() { close(s.ready) })
	s.logger.Info("USBIP server listening", "addr", s.config.Addr)
	// A persistent Accept failure (handle or socket exhaustion in the host process) used to spin
	// this loop on a core and write one line per iteration into a log that is never rotated.
	// Failures now back off up to a second and are logged on the first and every 60th occurrence.
	var acceptDelay time.Duration
	acceptFailures := 0
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || strings.Contains(strings.ToLower(err.Error()), "use of closed network connection") {
				s.logger.Info("USBIP server stopped")
				return nil
			}
			if acceptFailures%60 == 0 {
				s.logger.Error("Accept error", "error", err, "consecutive", acceptFailures+1)
			}
			acceptFailures++
			acceptDelay = min(max(2*acceptDelay, 5*time.Millisecond), time.Second)
			time.Sleep(acceptDelay)
			continue
		}
		acceptDelay = 0
		acceptFailures = 0
		if tcpConn, ok := c.(*net.TCPConn); ok {
			if err := tcpConn.SetNoDelay(true); err != nil {
				s.logger.Warn("failed to set TCP_NODELAY", "error", err)
			}
		}
		s.logger.Info("Client connected", "remote", c.RemoteAddr())
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logPanic(s.logger, "connection handler", r)
					_ = c.Close()
				}
			}()
			if err := s.handleConn(c); err != nil {
				if isClientDisconnect(err) {
					s.logger.Info("Client disconnected", "error", err)
				} else {
					s.logger.Error("Connection handler error", "error", err)
				}
			}
		}()
	}
}

// recoverAndLog logs a panic in a background goroutine instead of letting it
// abort the process that embeds the server. Use it directly with defer.
func recoverAndLog(logger *slog.Logger, where string) {
	if r := recover(); r != nil {
		logPanic(logger, where, r)
	}
}

func logPanic(logger *slog.Logger, where string, r any) {
	logger.Error("recovered panic", "where", where, "panic", r, "stack", string(debug.Stack()))
}

// Ready returns a channel that is closed once the server has successfully bound
// to its listen address and is ready to accept connections.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// ReadyErr returns the listener bind error after Ready() has unblocked a receiver, or nil when
// the server is actually listening. Reading it before Ready() closes is meaningless.
func (s *Server) ReadyErr() error { return s.readyErr }

// Close stops the USB server by closing its listener.
func (s *Server) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

// GetListenPort extracts and returns the port number from the server's listen address.
func (s *Server) GetListenPort() uint16 {
	addr := s.Addr()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(port)
}

// --

func (s *Server) handleConn(conn net.Conn) error {
	defer conn.Close() //nolint:errcheck
	conn = &logConn{Conn: conn, s: s}
	if err := conn.SetDeadline(time.Now().Add(s.config.ConnectionTimeout)); err != nil {
		s.logger.Warn("Failed to set deadline", "error", err)
	}

	// Peek first 8 bytes to determine management op or URB stream.
	var hdrBuf [headerPeekSize]byte
	if err := usbip.ReadExactly(conn, hdrBuf[:]); err != nil {
		return fmt.Errorf("read header: %w", err)
	}

	ver := binary.BigEndian.Uint16(hdrBuf[0:2])
	code := binary.BigEndian.Uint16(hdrBuf[2:4])

	if ver == usbip.Version && (code == usbip.OpReqDevlist || code == usbip.OpReqImport) {
		switch code {
		case usbip.OpReqDevlist:
			s.logger.Info("OP_REQ_DEVLIST")
			return s.handleDevList(conn)
		case usbip.OpReqImport:
			s.logger.Info("OP_REQ_IMPORT")
			dev, err := s.handleImport(conn)
			if err != nil {
				return fmt.Errorf("handle import: %w", err)
			}
			return s.handleUrbStream(conn, dev)
		}
	}

	return fmt.Errorf("protocol violation: client sent URB data without OP_REQ_IMPORT")
}

func (s *Server) handleDevList(conn net.Conn) error {
	_ = conn.SetDeadline(time.Time{})
	var buf bytes.Buffer
	rep := usbip.MgmtHeader{Version: usbip.Version, Command: usbip.OpRepDevlist, Status: 0}
	_ = rep.Write(&buf)
	metas := s.getAllDeviceMetas()
	n := uint32(len(metas))
	dlh := usbip.DevListReplyHeader{NDevices: n}
	_ = dlh.Write(&buf)
	for _, m := range metas {
		desc := m.Dev.GetDescriptor()
		meta := m.Meta

		exp := usbip.ExportedDevice{
			ExportMeta:          meta,
			Speed:               desc.Device.Speed,
			IDVendor:            desc.Device.IDVendor,
			IDProduct:           desc.Device.IDProduct,
			BcdDevice:           desc.Device.BcdDevice,
			BDeviceClass:        desc.Device.BDeviceClass,
			BDeviceSubClass:     desc.Device.BDeviceSubClass,
			BDeviceProtocol:     desc.Device.BDeviceProtocol,
			BConfigurationValue: usbConfigValueDefault,
			BNumConfigurations:  desc.Device.BNumConfigurations,
			BNumInterfaces:      desc.NumInterfaces(),
		}

		for _, iface := range descriptorListInterfaces(desc) {
			exp.Interfaces = append(exp.Interfaces, usbip.InterfaceDesc{
				Class:    iface.Descriptor.BInterfaceClass,
				SubClass: iface.Descriptor.BInterfaceSubClass,
				Protocol: iface.Descriptor.BInterfaceProtocol,
			})
		}
		_ = exp.WriteDevlist(&buf)
	}
	if _, err := conn.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write devlist: %w", err)
	}
	return nil
}

func (s *Server) handleImport(conn net.Conn) (usb.Device, error) {
	var rest [busIDSize]byte
	if err := usbip.ReadExactly(conn, rest[:]); err != nil {
		return nil, fmt.Errorf("read import busid: %w", err)
	}
	reqBus := string(rest[:bytes.IndexByte(rest[:], 0)])
	s.logger.Info("Import request", "busid", reqBus)
	var chosen usb.Device
	var chosenMeta *usbip.ExportMeta
	var chosenDesc *usb.Descriptor
	for _, m := range s.getAllDeviceMetas() {
		meta := m.Meta
		end := bytes.IndexByte(meta.USBBusID[:], 0)
		bid := string(meta.USBBusID[:end])
		if bid == reqBus {
			chosen = m.Dev
			chosenMeta = &meta
			chosenDesc = m.Dev.GetDescriptor()
			break
		}
	}
	if chosen == nil || chosenMeta == nil || chosenDesc == nil {
		return nil, fmt.Errorf("no device matches busid %s", reqBus)
	}
	var buf bytes.Buffer
	rep := usbip.MgmtHeader{Version: usbip.Version, Command: usbip.OpRepImport, Status: 0}
	_ = rep.Write(&buf)
	exp := usbip.ExportedDevice{
		ExportMeta:          *chosenMeta,
		Speed:               chosenDesc.Device.Speed,
		IDVendor:            chosenDesc.Device.IDVendor,
		IDProduct:           chosenDesc.Device.IDProduct,
		BcdDevice:           chosenDesc.Device.BcdDevice,
		BDeviceClass:        chosenDesc.Device.BDeviceClass,
		BDeviceSubClass:     chosenDesc.Device.BDeviceSubClass,
		BDeviceProtocol:     chosenDesc.Device.BDeviceProtocol,
		BConfigurationValue: usbConfigValueDefault,
		BNumConfigurations:  chosenDesc.Device.BNumConfigurations,
		BNumInterfaces:      chosenDesc.NumInterfaces(),
	}
	for _, iface := range descriptorListInterfaces(chosenDesc) {
		exp.Interfaces = append(exp.Interfaces, usbip.InterfaceDesc{
			Class:    iface.Descriptor.BInterfaceClass,
			SubClass: iface.Descriptor.BInterfaceSubClass,
			Protocol: iface.Descriptor.BInterfaceProtocol,
		})
	}
	_ = exp.WriteImport(&buf)
	if _, err := conn.Write(buf.Bytes()); err != nil {
		return nil, fmt.Errorf("write import reply failed: %w", err)
	}
	return chosen, nil
}

// getAllDeviceMetas aggregates device metas from all registered busses.
func (s *Server) getAllDeviceMetas() []virtualbus.DeviceMeta {
	s.busesMu.Lock()
	defer s.busesMu.Unlock()
	out := []virtualbus.DeviceMeta{}
	for _, b := range s.busses {
		out = append(out, b.GetAllDeviceMetas()...)
	}
	return out
}

type logConn struct {
	net.Conn
	s *Server
}

func (lc *logConn) Read(p []byte) (int, error) {
	n, err := lc.Conn.Read(p)
	if n > 0 && lc.s.rawLogger != nil {
		lc.s.rawLogger.Log(true, p[:n])
	}
	return n, err
}

func (lc *logConn) Write(p []byte) (int, error) {
	n, err := lc.Conn.Write(p)
	if n > 0 && lc.s.rawLogger != nil {
		lc.s.rawLogger.Log(false, p[:n])
	}
	return n, err
}

func (s *Server) handleUrbStream(conn net.Conn, dev usb.Device) error {
	_ = conn.SetDeadline(time.Time{})

	var writer io.Writer
	var bw *batchingWriter
	if s.config.WriteBatchFlushInterval > 0 {
		bw = newBatchingWriter(conn, writeBatcherBufferSize, s.config.WriteBatchFlushInterval, writeBatcherFlushAtBytes)
		writer = bw
		defer func() { _ = bw.Close() }()
	} else {
		writer = conn
	}

	var owningBus *virtualbus.VirtualBus
	for _, b := range s.busses {
		devices := b.Devices()
		if slices.Contains(devices, dev) {
			owningBus = b
		}
		if owningBus != nil {
			break
		}
	}
	if owningBus == nil {
		return fmt.Errorf("device does not belong to any bus")
	}

	ctx := owningBus.GetDeviceContext(dev)
	if ctx == nil {
		return fmt.Errorf("no device context available from bus")
	}

	// Data-driven completion plumbing (ported from upstream 7e33d2d3):
	// interrupt-IN URBs are completed asynchronously when the device has
	// FRESH input (device HandleTransfer blocks on its input channel), so
	// completions can interleave with the synchronous EP0/OUT path below —
	// writeRet serializes the wire writes.
	var writeMu sync.Mutex
	retOut := make([]byte, usbip.HeaderSize, usbip.HeaderSize+64)
	writeRet := func(seq uint32, status int32, actualLen uint32, respData []byte, flush bool) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		ret := usbip.RetSubmit{
			Basic:           usbip.HeaderBasic{Command: usbip.RetSubmitCode, Seqnum: seq, Devid: 0, Dir: 0, Ep: 0},
			Status:          status,
			ActualLength:    actualLen,
			StartFrame:      0,
			NumberOfPackets: 0,
			ErrorCount:      0,
		}
		ret.Encode(retOut[:usbip.HeaderSize])
		retOut = append(retOut[:usbip.HeaderSize], respData...)
		if _, err := writer.Write(retOut); err != nil {
			return fmt.Errorf("write RET_SUBMIT: %w", err)
		}
		if flush && bw != nil {
			if err := bw.Flush(); err != nil {
				return fmt.Errorf("flush response: %w", err)
			}
		}
		return nil
	}

	// In-flight interrupt-IN URBs by seqnum, so UNLINK can cancel them. A URB is unlinked by
	// removing it from here: its worker takes it out of the map before completing it and drops
	// one that is already gone. Only the generic worker below waits on a context per URB and
	// records its cancel; the data-driven worker is woken through its endpoint instead.
	type pendingURB struct {
		ep     uint32
		cancel context.CancelFunc
	}
	var pendingMu sync.Mutex
	pending := map[uint32]pendingURB{}
	defer func() {
		pendingMu.Lock()
		for _, urb := range pending {
			if urb.cancel != nil {
				urb.cancel()
			}
		}
		clear(pending)
		pendingMu.Unlock()
	}()
	// takePending removes a URB and reports whether it was still in flight. One that is gone was
	// unlinked or belongs to a stream being torn down, and must not be completed.
	takePending := func(seq uint32) bool {
		pendingMu.Lock()
		_, ok := pending[seq]
		if ok {
			delete(pending, seq)
		}
		pendingMu.Unlock()
		return ok
	}
	stillPending := func(seq uint32) bool {
		pendingMu.Lock()
		_, ok := pending[seq]
		pendingMu.Unlock()
		return ok
	}

	// streamDone releases the endpoint workers when the URB stream ends for any reason, including
	// a read error on a connection whose device is still present.
	streamDone := make(chan struct{})

	// Persistent per-endpoint completion workers: one goroutine per interrupt-IN endpoint for the
	// lifetime of the URB stream, fed by a small job queue, instead of one goroutine per URB.
	// Each worker owns a reusable frame buffer (RET_SUBMIT header and payload assembled and
	// written as a single syscall) and its endpoint's last report, sent again on bInterval expiry
	// with no fresh input so the host still sees its poll-rate keepalive.
	//
	// A device that implements usb.InterruptInSource is served without a context or allocation
	// per poll: the worker waits on the endpoint's own input channel and a reusable timer, and
	// completes each URB by encoding the endpoint's current state into the frame it owns. Every
	// other device goes through HandleTransfer with a context per URB.
	type inJob struct {
		seq uint32
		// ctx and cancel are set on the generic path only, where the device waits on them.
		ctx    context.Context
		cancel context.CancelFunc
	}
	type inEndpoint struct {
		jobs chan inJob
		// wake releases a data-driven worker that is waiting, so an UNLINK does not sit out the
		// poll interval. It is nil on the generic path, which cancels the URB's context instead.
		wake chan struct{}
	}
	inEndpoints := map[uint32]*inEndpoint{}
	defer func() {
		close(streamDone)
		for _, endpoint := range inEndpoints {
			close(endpoint.jobs)
		}
	}()
	startInWorker := func(ep uint32) *inEndpoint {
		endpoint := &inEndpoint{jobs: make(chan inJob, 8)}
		jobs := endpoint.jobs
		interval := endpointInterval(dev.GetDescriptor(), ep)
		hwPaced := s.config.HardwarePacedCompletions && interval > 0
		// NAK-idle: no poll deadline — the endpoint stays pending until real input (the pacer
		// still enforces bInterval spacing), so nothing is replayed and idle endpoints stay
		// dormant. In "auto" the device can declare this per endpoint or for the whole device.
		nakIdle := interruptInNAKIdle(dev, ep, s.config.IdleMode)
		// How long a keepalive endpoint waits before repeating a report the host already has.
		// The first repeat still comes one bInterval after the last fresh input, so a device
		// that streams looks unchanged to a consumer watching the report rate; only an endpoint
		// that has already gone quiet slows down.
		idleInterval := s.config.IdleKeepaliveInterval
		if idleInterval < interval {
			idleInterval = interval
		}

		// Hardware pacing: completions are held to the endpoint's poll cadence (anchored,
		// drift-free). The input gate coalesces updates that land between polls, latest state
		// wins — the same thing a real controller's bInterval does. nextDue and the pacer belong
		// to the one worker started here.
		var nextDue time.Time
		var pacer *time.Timer
		if hwPaced {
			pacer = time.NewTimer(time.Hour)
			if !pacer.Stop() {
				<-pacer.C
			}
		}
		// pace waits out the rest of the endpoint's poll interval before the next completion is
		// even considered. It reports false when that wait was cut short.
		pace := func(cancelled <-chan struct{}) bool {
			if !hwPaced {
				return true
			}
			now := time.Now()
			if !nextDue.IsZero() && nextDue.After(now) {
				pacer.Reset(nextDue.Sub(now))
				select {
				case <-pacer.C:
				case <-cancelled:
					if !pacer.Stop() {
						<-pacer.C
					}
					return false
				}
			}
			now = time.Now()
			if nextDue.IsZero() || nextDue.Add(interval).Before(now) {
				nextDue = now.Add(interval)
			} else {
				nextDue = nextDue.Add(interval)
			}
			return true
		}
		// A panicking worker ends the stream: the connection is closed so the reader returns, and
		// the queue is drained so a sender blocked on it is released.
		recoverWorker := func() {
			if r := recover(); r != nil {
				logPanic(s.logger, "interrupt-IN worker", r)
				_ = conn.Close()
				for range jobs {
				}
			}
		}
		// complete writes one RET_SUBMIT with its payload as a single syscall.
		complete := func(frame []byte, seq uint32, payloadLen int) {
			ret := usbip.RetSubmit{
				Basic:           usbip.HeaderBasic{Command: usbip.RetSubmitCode, Seqnum: seq, Devid: 0, Dir: 0, Ep: 0},
				Status:          0,
				ActualLength:    uint32(payloadLen),
				StartFrame:      0,
				NumberOfPackets: 0,
				ErrorCount:      0,
			}
			ret.Encode(frame[:usbip.HeaderSize])
			writeMu.Lock()
			_, werr := writer.Write(frame[:usbip.HeaderSize+payloadLen])
			if werr == nil && bw != nil {
				werr = bw.Flush()
			}
			writeMu.Unlock()
			if werr != nil {
				if isClientDisconnect(werr) {
					s.logger.Debug("URB completion after disconnect", "seq", seq, "error", werr)
				} else {
					s.logger.Error("write async RET_SUBMIT", "seq", seq, "error", werr)
				}
			}
		}

		source, _ := dev.(usb.InterruptInSource)
		var signal <-chan struct{}
		if source != nil {
			signal = source.InputSignal(ep)
		}
		// An endpoint that offers no input channel can be served this way only when it may stay
		// silent; with a poll deadline there would be nothing to build the first report from, so
		// such an endpoint goes through HandleTransfer instead.
		if source != nil && (signal != nil || nakIdle) {
			endpoint.wake = make(chan struct{}, 1)
			wake := endpoint.wake
			go func() {
				defer recoverWorker()
				frame := make([]byte, usbip.HeaderSize+maxInterruptPayload)
				payload := frame[usbip.HeaderSize:]
				// repeated tracks whether the last completion already carried a report the host
				// had seen, which is what puts the endpoint on the idle interval.
				repeated := false
				completed := false
				poll := time.NewTimer(time.Hour)
				if !poll.Stop() {
					<-poll.C
				}
				defer poll.Stop()

				for job := range jobs {
					if !stillPending(job.seq) {
						// Unlinked before its turn came: waiting on it would hold up the URBs
						// queued behind it, and in NAK-idle mode that wait has no deadline.
						continue
					}
					if !pace(streamDone) {
						return
					}
					polling := !nakIdle && interval > 0

					// How long this URB may wait for fresh input before the report goes out with
					// the state on hand.
					//
					// With hardware pacing, pace has just waited out the bInterval, so the poll is
					// now and the wait is zero: the endpoint reports on its own grid with whatever
					// state it has, the way a host poll reads a real device, and input that lands
					// after this poll rides the next one. Completing the moment input arrives
					// instead would put the report stream on the consumer's sample cadence, and
					// when that cadence does not divide the bInterval the host sees a held report
					// and a fresh one a fraction of a millisecond apart every few samples. Against
					// live Steam that was a gyro microstutter: with a 125 Hz gyro on the 6 ms Deck
					// endpoint, a quarter of all reports arrived under 3 ms after the previous one
					// (2026-09-26). Only an endpoint that has already repeated itself waits
					// longer, for the rest of the idle interval, and fresh input ends that wait.
					//
					// Without hardware pacing the poll interval itself is the wait, as before.
					var wait time.Duration
					if polling {
						switch {
						case !hwPaced && repeated:
							wait = idleInterval
						case !hwPaced:
							wait = interval
						case repeated && idleInterval > interval:
							wait = idleInterval - interval
						}
					}
					fresh, send := false, false
					if polling && wait == 0 {
						select {
						case <-signal:
							fresh = true
						default:
						}
						send = true
					}
					armed := false
					if !send && polling {
						poll.Reset(wait)
						armed = true
					}
				wait:
					for !send {
						select {
						case <-signal:
							fresh, send = true, true
						case <-poll.C:
							// The wait ended with no fresh input: the host gets the state the
							// endpoint has, which is what its bInterval promises.
							armed, send = false, true
						case <-wake:
							// An UNLINK landed on this endpoint; it may be this URB.
							if !stillPending(job.seq) {
								break wait
							}
						case <-ctx.Done():
							// The device was removed; the stream ends with it.
							return
						case <-streamDone:
							return
						}
					}
					if armed && !poll.Stop() {
						<-poll.C
					}
					if !send {
						continue
					}
					// Every completion encodes the current state, so the device stamps each report
					// the way its firmware would, whether or not the state behind it changed.
					n, ok := source.WriteInputReport(ep, payload)
					if !ok || !takePending(job.seq) {
						continue
					}
					repeated = completed && !fresh
					completed = true
					complete(frame, job.seq, n)
				}
			}()
			return endpoint
		}

		go func() {
			defer recoverWorker()
			frame := make([]byte, usbip.HeaderSize, usbip.HeaderSize+maxInterruptPayload)
			var last []byte
			haveLast := false
			repeated := false
			for job := range jobs {
				if !pace(job.ctx.Done()) {
					takePending(job.seq)
					job.cancel()
					continue
				}
				var respData []byte
				for {
					attemptCtx, attemptCancel := job.ctx, context.CancelFunc(func() {})
					if !nakIdle && interval > 0 {
						wait := interval
						if repeated {
							wait = idleInterval
						}
						attemptCtx, attemptCancel = context.WithTimeout(job.ctx, wait)
					}
					respData = s.processSubmit(attemptCtx, dev, ep, usbip.DirIn, nil, nil)
					expired := respData == nil && errors.Is(attemptCtx.Err(), context.DeadlineExceeded)
					attemptCancel()

					if job.ctx.Err() != nil {
						respData = nil
						break
					}
					if respData != nil {
						last = append(last[:0], respData...)
						haveLast = true
						repeated = false
						break
					}
					if expired {
						if haveLast {
							respData = last
							repeated = true
							break
						}
						continue
					}
					// Device answered "no data" without blocking.
					break
				}

				unlinked := !takePending(job.seq)
				job.cancel()
				if unlinked {
					// Unlinked or stream torn down mid-wait: no completion.
					continue
				}

				frame = append(frame[:usbip.HeaderSize], respData...)
				complete(frame, job.seq, len(respData))
			}
		}()
		return endpoint
	}

	var outPayloadScratch []byte

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("device removed, closing URB stream")
			busID := owningBus.BusID()
			if emptyCtx := owningBus.GetBusEmptyContext(); emptyCtx != nil {
				go func() {
					defer recoverAndLog(s.logger, "bus cleanup")
					slog.Debug("Started bus cleanup goroutine (HandleUrbStream ctx.Done)")
					select {
					case <-emptyCtx.Done():
						// Cancelled - a new device was added
						return
					case <-time.After(s.config.BusCleanupTimeout):
						if b := s.GetBus(busID); b != nil && len(b.Devices()) == 0 {
							if err := s.RemoveBus(busID); err != nil {
								s.logger.Error("timeout: failed to remove empty bus", "busID", busID, "error", err)
							} else {
								s.logger.Info("timeout: removed empty bus", "busID", busID)
							}
						}
					}
				}()
			} else {
				s.logger.Debug("No bus empty context; Cleaning bus immediately")
				if b := s.GetBus(busID); b != nil && len(b.Devices()) == 0 {
					if err := s.RemoveBus(busID); err != nil {
						s.logger.Error("timeout: failed to remove empty bus", "busID", busID, "error", err)
					} else {
						s.logger.Info("timeout: removed empty bus", "busID", busID)
					}
				}
			}
			return nil
		default:
		}

		var hdr [urbHdrSize]byte
		if err := usbip.ReadExactly(conn, hdr[:]); err != nil {
			return fmt.Errorf("read URB header: %w", err)
		}
		cmd := binary.BigEndian.Uint32(hdr[urbHdrOffsetCommand : urbHdrOffsetCommand+4])
		seq := binary.BigEndian.Uint32(hdr[urbHdrOffsetSeqnum : urbHdrOffsetSeqnum+4])
		dir := binary.BigEndian.Uint32(hdr[urbHdrOffsetDir : urbHdrOffsetDir+4])
		ep := binary.BigEndian.Uint32(hdr[urbHdrOffsetEp : urbHdrOffsetEp+4])
		if cmd == usbip.CmdUnlinkCode {
			unlinkSeq := binary.BigEndian.Uint32(hdr[urbHdrOffsetUnlink : urbHdrOffsetUnlink+4])
			s.logger.Debug("USBIP_CMD_UNLINK", "seq", seq, "unlink", unlinkSeq)
			pendingMu.Lock()
			urb, found := pending[unlinkSeq]
			if found {
				delete(pending, unlinkSeq)
			}
			pendingMu.Unlock()
			// -ECONNRESET signals the URB was unlinked before completion;
			// status 0 means it already completed normally.
			status := int32(0)
			if found {
				if urb.cancel != nil {
					urb.cancel()
				} else if endpoint := inEndpoints[urb.ep]; endpoint != nil {
					select {
					case endpoint.wake <- struct{}{}:
					default:
					}
				}
				status = errConnReset
			}
			ret := usbip.RetUnlink{Basic: usbip.HeaderBasic{Command: usbip.RetUnlinkCode, Seqnum: seq, Devid: 0, Dir: 0, Ep: 0}, Status: status}
			writeMu.Lock()
			_ = ret.Write(writer)
			if bw != nil {
				_ = bw.Flush()
			}
			writeMu.Unlock()
			continue
		}
		if cmd != usbip.CmdSubmitCode {
			devid := binary.BigEndian.Uint32(hdr[urbHdrOffsetDevid : urbHdrOffsetDevid+4])
			return fmt.Errorf("unsupported cmd %d (seq=%d, devid=%d)", cmd, seq, devid)
		}
		xferLen := binary.BigEndian.Uint32(hdr[urbHdrOffsetLength : urbHdrOffsetLength+4])
		setup := hdr[urbHdrOffsetSetup:urbHdrSize]

		var outPayload []byte
		if dir == usbip.DirOut && xferLen > 0 {
			if cap(outPayloadScratch) < int(xferLen) {
				outPayloadScratch = make([]byte, xferLen)
			}
			outPayload = outPayloadScratch[:xferLen]
			if err := usbip.ReadExactly(conn, outPayload); err != nil {
				return fmt.Errorf("read OUT payload: %w", err)
			}
		}

		if dir == usbip.DirIn && ep != 0 {
			// Data-driven interrupt-IN: hand the URB to the endpoint's persistent worker, which
			// completes it when the endpoint has FRESH input (or sends its last report again on
			// bInterval expiry).
			endpoint := inEndpoints[ep]
			if endpoint == nil {
				endpoint = startInWorker(ep)
				inEndpoints[ep] = endpoint
			}
			job := inJob{seq: seq}
			urb := pendingURB{ep: ep}
			if endpoint.wake == nil {
				// The generic worker hands the URB's context to the device.
				job.ctx, job.cancel = context.WithCancel(ctx)
				urb.cancel = job.cancel
			}
			pendingMu.Lock()
			pending[seq] = urb
			pendingMu.Unlock()
			endpoint.jobs <- job
			continue
		}

		// EP0 and OUT transfers never block and are handled in order.
		respData := s.processSubmit(ctx, dev, ep, dir, setup, outPayload)

		// STALL (EPIPE) when GET_DESCRIPTOR returns no data — required by USB spec
		// for unsupported descriptor types (e.g. device_qualifier for full-speed devices).
		// MS-GIPUSB §2.2.2: GIP devices MUST STALL device_qualifier requests.
		var urbStatus int32
		if ep == 0 && len(respData) == 0 && len(setup) == 8 {
			bm := setup[0]
			breq := setup[1]
			if (bm == 0x80 || bm == 0x81) && breq == usbReqGetDescriptor {
				urbStatus = -32 // -EPIPE = STALL
			}
		}

		actualLen := uint32(len(respData))
		if dir == usbip.DirOut {
			actualLen = uint32(len(outPayload))
		}
		if err := writeRet(seq, urbStatus, actualLen, respData, ep == 0); err != nil {
			return err
		}
	}
}

// Composite devices may stream controller reports while leaving placeholder
// endpoints pending. Applying the controller's keepalive timeout to those
// endpoints wakes a timer repeatedly even though they never return a report.
func interruptInNAKIdle(dev usb.Device, ep uint32, mode string) bool {
	switch mode {
	case "nak":
		return true
	case "keepalive":
		return false
	}
	if endpoint, ok := dev.(interface{ NaksWhenIdleForEndpoint(uint32) bool }); ok {
		return endpoint.NaksWhenIdleForEndpoint(ep)
	}
	if device, ok := dev.(interface{ NaksWhenIdle() bool }); ok {
		return device.NaksWhenIdle()
	}
	return false
}

// endpointInterval returns the polling interval of the given interrupt IN
// endpoint (from its descriptor's bInterval), or 0 when unknown.
func endpointInterval(desc *usb.Descriptor, ep uint32) time.Duration {
	epAddr := uint8(ep) | 0x80
	for i := range desc.Interfaces {
		for _, epDesc := range desc.Interfaces[i].Endpoints {
			if epDesc.BEndpointAddress != epAddr || epDesc.BMAttributes&0x03 != 0x03 {
				continue
			}
			return time.Duration(epDesc.BInterval) * time.Millisecond
		}
	}
	return 0
}

// isClientDisconnect tests whether an error represents a normal client
// disconnect (EOF, ECONNRESET, broken pipe, or the Windows WSAECONNRESET
// translated error). We treat those as normal client disconnects and log
// them at Info level instead of Error.
func isClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		switch t := opErr.Err.(type) {
		case syscall.Errno:
			if t == syscall.ECONNRESET || t == syscall.EPIPE {
				return true
			}
		}
	}
	e := strings.ToLower(err.Error())
	if strings.Contains(e, "connection reset by peer") || strings.Contains(e, "forcibly closed") || strings.Contains(e, "an existing connection was forcibly closed") || strings.Contains(e, "aborted") {
		return true
	}
	return false
}

func (s *Server) processSubmit(ctx context.Context, dev usb.Device, ep uint32, dir uint32, setup []byte, out []byte) []byte {
	if ep != 0 {
		return dev.HandleTransfer(ctx, ep, dir, out)
	}
	if len(setup) != 8 {
		s.logger.Debug("EP0 submit with invalid setup size", "setupLen", len(setup), "setup", setup)
		return nil
	}
	bm := setup[0]
	breq := setup[1]
	wValue := binary.LittleEndian.Uint16(setup[2:4])
	wIndex := binary.LittleEndian.Uint16(setup[4:6])
	wLength := binary.LittleEndian.Uint16(setup[6:8])

	if breq == usbReqGetStatus {
		return []byte{0x00, 0x00}
	}
	if breq == usbReqSetAddress && bm == usbReqTypeStandardToDevice {
		return nil
	}
	if breq == usbReqSetConfiguration && bm == usbReqTypeStandardToDevice {
		return nil
	}
	if breq == usbReqGetConfiguration && bm == usbReqTypeStandardFromDevice {
		return []byte{0x01}
	}

	desc := dev.GetDescriptor()

	if breq == usbReqGetDescriptor && bm == usbReqTypeStandardFromDevice {
		dtype := uint8(wValue >> 8)
		dindex := uint8(wValue & 0xff)
		var data []byte
		switch dtype {
		case usbDescTypeDevice:
			data = desc.Bytes()
		case usbDescTypeConfiguration:
			data = s.buildConfigDescriptor(desc)
		case usbDescTypeString:
			if dindex == 0xEE && desc.MicrosoftOS10 != nil {
				data = desc.MicrosoftOS10.StringDescriptor()
			} else if s, ok := desc.Strings[dindex]; ok {
				data = usb.EncodeStringDescriptor(s)
			}
		}
		if len(data) == 0 {
			return nil
		}
		if int(wLength) < len(data) {
			return data[:wLength]
		}
		return data
	}

	if desc.MicrosoftOS10 != nil &&
		(bm == 0xC0 || bm == 0xC1) &&
		(breq == desc.MicrosoftOS10.EffectiveVendorCode() ||
			wIndex == 0x0004 || wIndex == 0x0005) {
		if data, ok := desc.MicrosoftOS10.ControlResponse(wValue, wIndex); ok {
			if int(wLength) < len(data) {
				return data[:wLength]
			}
			return data
		}
	}

	if breq == usbReqGetDescriptor && bm == usbReqTypeStandardToInterface {
		dtype := uint8(wValue >> 8)
		iface := uint8(wIndex & 0xff)
		var data []byte
		if ifaceConf, ok := desc.Interface(iface); ok {
			if ifaceConf.HID != nil {
				switch dtype {
				case usbDescTypeHID:
					d, err := ifaceConf.HID.DescriptorBytes()
					if err != nil {
						s.logger.Error("failed to build HID descriptor", "iface", iface, "error", err)
						return nil
					}
					data = []byte(d)
				case usbDescTypeHIDReport:
					d, err := ifaceConf.HID.ReportBytes()
					if err != nil {
						s.logger.Error("failed to build HID report descriptor", "iface", iface, "error", err)
						return nil
					}
					data = []byte(d)
				}
			}
			if len(data) == 0 {
				for _, cd := range ifaceConf.ClassDescriptors {
					if cd.DescriptorType == dtype {
						data = []byte(cd.Bytes())
						break
					}
				}
			}
		}
		if len(data) == 0 {
			return nil
		}
		if int(wLength) < len(data) {
			return data[:wLength]
		}
		return data
	}

	if cd, ok := dev.(usb.ControlDevice); ok {
		if resp, handled := cd.HandleControl(bm, breq, wValue, wIndex, wLength, out); handled {
			if resp == nil {
				return nil
			}
			if int(wLength) < len(resp) {
				return resp[:wLength]
			}
			return resp
		}
	}

	if iface := int(wIndex & usbIfaceIndexMask); iface >= 0 && iface < len(desc.Interfaces) {
		if desc.Interfaces[iface].Descriptor.BInterfaceClass == usbInterfaceClassHID {
			switch {
			case bm == hidReqTypeIn && breq == hidReqGetIdle:
				return []byte{0x00}
			case bm == hidReqTypeOut && breq == hidReqSetIdle:
				return nil
			case bm == hidReqTypeIn && breq == hidReqGetProtocol:
				return []byte{0x01}
			case bm == hidReqTypeOut && breq == hidReqSetProtocol:
				return nil
			case (bm == hidReqTypeIn || bm == hidReqTypeOut) && (breq == hidReqGetReport || breq == hidReqSetReport):
				return nil
			}
		}
	}

	if (bm & usbReqTypeMask) != usbReqTypeClass {
		s.logger.Debug("EP0 control unhandled", "bmRequestType", bm, "bRequest", breq, "wValue", wValue, "wIndex", wIndex, "wLength", wLength)
	}

	return nil
}

func (s *Server) buildConfigDescriptor(desc *usb.Descriptor) []byte {
	var b bytes.Buffer
	configValue := desc.Configuration.BConfigurationValue
	if configValue == 0 {
		configValue = usbConfigValueDefault
	}
	attrs := desc.Configuration.BMAttributes
	if attrs == 0 {
		attrs = usbConfigAttrBusPowered
	}
	maxPower := desc.Configuration.BMaxPower
	if maxPower == 0 {
		maxPower = usbConfigMaxPower100mA
	}
	h := usb.ConfigHeader{
		WTotalLength:        0, // to be patched
		BNumInterfaces:      desc.NumInterfaces(),
		BConfigurationValue: configValue,
		IConfiguration:      desc.Configuration.IConfiguration,
		BMAttributes:        attrs,
		BMaxPower:           maxPower,
	}
	h.Write(&b)
	for _, iface := range desc.Interfaces {
		for _, iad := range desc.Associations {
			if iad.BFirstInterface == iface.Descriptor.BInterfaceNumber && iface.Descriptor.BAlternateSetting == 0 {
				iad.Write(&b)
			}
		}
		iface.Descriptor.Write(&b)
		if iface.HID != nil {
			hd, err := iface.HID.DescriptorBytes()
			if err != nil {
				s.logger.Error("failed to build HID descriptor", "iface", iface.Descriptor.BInterfaceNumber, "error", err)
				// Stall/return minimal config descriptor.
				return nil
			}
			b.Write([]byte(hd))
		}
		for _, cd := range iface.ClassDescriptors {
			b.Write([]byte(cd.Bytes()))
		}
		for _, ep := range iface.Endpoints {
			ep.Write(&b)
			for _, cd := range ep.ClassDescriptors {
				b.Write([]byte(cd.Bytes()))
			}
		}
	}

	data := b.Bytes()
	binary.LittleEndian.PutUint16(data[2:4], uint16(len(data)))
	return data
}

func descriptorListInterfaces(desc *usb.Descriptor) []usb.InterfaceConfig {
	out := make([]usb.InterfaceConfig, 0, desc.NumInterfaces())
	seen := map[uint8]struct{}{}
	for _, iface := range desc.Interfaces {
		n := iface.Descriptor.BInterfaceNumber
		if _, ok := seen[n]; ok || iface.Descriptor.BAlternateSetting != 0 {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, iface)
	}
	for _, iface := range desc.Interfaces {
		n := iface.Descriptor.BInterfaceNumber
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, iface)
	}
	return out
}
