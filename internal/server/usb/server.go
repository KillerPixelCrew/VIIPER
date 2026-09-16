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
	retSubmitHeaderSize = 0x30

	// avoid windows socket overhead while keeping latency very low.
	writeBatcherBufferSize   = 256 * 1024
	writeBatcherFlushAtBytes = 64 * 1024
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
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || strings.Contains(strings.ToLower(err.Error()), "use of closed network connection") {
				s.logger.Info("USBIP server stopped")
				return nil
			}
			s.logger.Error("Accept error", "error", err)
			continue
		}
		if tcpConn, ok := c.(*net.TCPConn); ok {
			if err := tcpConn.SetNoDelay(true); err != nil {
				s.logger.Warn("failed to set TCP_NODELAY", "error", err)
			}
		}
		s.logger.Info("Client connected", "remote", c.RemoteAddr())
		go func() {
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
	defer conn.Close()
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
			dev, err := s.handleImport(conn, hdrBuf[:])
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
			BNumInterfaces:      interfaceCount(desc.Interfaces),
		}

		for _, iface := range usbipInterfaceDescs(desc.Interfaces) {
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

func (s *Server) handleImport(conn net.Conn, first8 []byte) (usb.Device, error) {
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
		end := bytes.IndexByte(meta.USBBusId[:], 0)
		bid := string(meta.USBBusId[:end])
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
		BNumInterfaces:      interfaceCount(chosenDesc.Interfaces),
	}
	for _, iface := range usbipInterfaceDescs(chosenDesc.Interfaces) {
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

type readBufferConn struct {
	net.Conn
	buf []byte
}

func (r *readBufferConn) Read(p []byte) (int, error) {
	if len(r.buf) > 0 {
		n := copy(p, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}
	return r.Conn.Read(p)
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
		for _, d := range devices {
			if d == dev {
				owningBus = b
				break
			}
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
	var retOut bytes.Buffer
	retOut.Grow(retSubmitHeaderSize)
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
		retOut.Reset()
		if err := ret.Write(&retOut); err != nil {
			return fmt.Errorf("build RET_SUBMIT header: %w", err)
		}
		if _, err := writer.Write(retOut.Bytes()); err != nil {
			return fmt.Errorf("write RET_SUBMIT: %w", err)
		}
		if len(respData) > 0 {
			if _, err := writer.Write(respData); err != nil {
				return fmt.Errorf("write RET_SUBMIT payload: %w", err)
			}
		}
		if flush && bw != nil {
			if err := bw.Flush(); err != nil {
				return fmt.Errorf("flush response: %w", err)
			}
		}
		return nil
	}

	// In-flight interrupt-IN URBs by seqnum, so UNLINK can cancel them.
	var pendingMu sync.Mutex
	pending := map[uint32]context.CancelFunc{}
	defer func() {
		pendingMu.Lock()
		for _, cancel := range pending {
			cancel()
		}
		pendingMu.Unlock()
	}()

	// Persistent per-endpoint completion workers: one goroutine per
	// interrupt-IN endpoint for the lifetime of the URB stream, fed by a
	// small job queue, instead of one goroutine per URB. Each worker owns a
	// reusable frame buffer (RET_SUBMIT header + payload assembled and
	// written as a single syscall) and its endpoint's last-response cache
	// (replayed on bInterval expiry with no fresh input, so the host still
	// sees its poll-rate keepalive).
	type inJob struct {
		seq    uint32
		ctx    context.Context
		cancel context.CancelFunc
	}
	inWorkers := map[uint32]chan inJob{}
	defer func() {
		for _, ch := range inWorkers {
			close(ch)
		}
	}()
	startInWorker := func(ep uint32) chan inJob {
		jobs := make(chan inJob, 8)
		interval := endpointInterval(dev.GetDescriptor(), ep)
		hwPaced := s.config.HardwarePacedCompletions && interval > 0
		// NAK-idle: no per-attempt deadline — the device blocks on its gate
		// until real input (the pacer still enforces bInterval spacing), so
		// nothing is replayed and idle endpoints stay dormant. In "auto" the
		// device can declare this per endpoint or for the whole device.
		nakIdle := interruptInNAKIdle(dev, ep, s.config.IdleMode)
		go func() {
			var frame bytes.Buffer
			var last []byte
			haveLast := false
			// Hardware pacing: completions are held to the endpoint's poll
			// cadence (anchored, drift-free). The input gate coalesces
			// updates that land between polls, latest state wins — the same
			// thing a real controller's bInterval does.
			var nextDue time.Time
			var pacer *time.Timer
			if hwPaced {
				pacer = time.NewTimer(time.Hour)
				if !pacer.Stop() {
					<-pacer.C
				}
			}
			for job := range jobs {
				if hwPaced {
					now := time.Now()
					if !nextDue.IsZero() && nextDue.After(now) {
						pacer.Reset(nextDue.Sub(now))
						select {
						case <-pacer.C:
						case <-job.ctx.Done():
							if !pacer.Stop() {
								<-pacer.C
							}
						}
					}
					now = time.Now()
					if nextDue.IsZero() || nextDue.Add(interval).Before(now) {
						nextDue = now.Add(interval)
					} else {
						nextDue = nextDue.Add(interval)
					}
				}
				var respData []byte
				for {
					attemptCtx, attemptCancel := job.ctx, context.CancelFunc(func() {})
					if !nakIdle && interval > 0 {
						attemptCtx, attemptCancel = context.WithTimeout(job.ctx, interval)
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
						break
					}
					if expired {
						if haveLast {
							respData = last
							break
						}
						continue
					}
					// Device answered "no data" without blocking.
					break
				}

				pendingMu.Lock()
				delete(pending, job.seq)
				pendingMu.Unlock()
				job.cancel()

				if job.ctx.Err() != nil && respData == nil {
					// Unlinked or stream torn down mid-wait: no completion.
					continue
				}

				frame.Reset()
				ret := usbip.RetSubmit{
					Basic:           usbip.HeaderBasic{Command: usbip.RetSubmitCode, Seqnum: job.seq, Devid: 0, Dir: 0, Ep: 0},
					Status:          0,
					ActualLength:    uint32(len(respData)),
					StartFrame:      0,
					NumberOfPackets: 0,
					ErrorCount:      0,
				}
				if err := ret.Write(&frame); err != nil {
					s.logger.Error("build async RET_SUBMIT", "seq", job.seq, "error", err)
					continue
				}
				frame.Write(respData)
				writeMu.Lock()
				_, werr := writer.Write(frame.Bytes())
				if werr == nil && bw != nil {
					werr = bw.Flush()
				}
				writeMu.Unlock()
				if werr != nil {
					if isClientDisconnect(werr) {
						s.logger.Debug("URB completion after disconnect", "seq", job.seq, "error", werr)
					} else {
						s.logger.Error("write async RET_SUBMIT", "seq", job.seq, "error", werr)
					}
				}
			}
		}()
		return jobs
	}

	var outPayloadScratch []byte

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("device removed, closing URB stream")
			busID := owningBus.BusID()
			if emptyCtx := owningBus.GetBusEmptyContext(); emptyCtx != nil {
				go func() {
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
		devid := binary.BigEndian.Uint32(hdr[urbHdrOffsetDevid : urbHdrOffsetDevid+4])
		dir := binary.BigEndian.Uint32(hdr[urbHdrOffsetDir : urbHdrOffsetDir+4])
		ep := binary.BigEndian.Uint32(hdr[urbHdrOffsetEp : urbHdrOffsetEp+4])
		if cmd == usbip.CmdUnlinkCode {
			unlinkSeq := binary.BigEndian.Uint32(hdr[urbHdrOffsetUnlink : urbHdrOffsetUnlink+4])
			s.logger.Debug("USBIP_CMD_UNLINK", "seq", seq, "unlink", unlinkSeq)
			pendingMu.Lock()
			cancel, found := pending[unlinkSeq]
			if found {
				delete(pending, unlinkSeq)
			}
			pendingMu.Unlock()
			// -ECONNRESET signals the URB was unlinked before completion;
			// status 0 means it already completed normally.
			status := int32(0)
			if found {
				cancel()
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
			return fmt.Errorf("unsupported cmd %d (seq=%d, devid=%d)", cmd, seq, devid)
		}
		xferFlags := binary.BigEndian.Uint32(hdr[urbHdrOffsetFlags : urbHdrOffsetFlags+4])
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
			// Data-driven interrupt-IN: hand the URB to the endpoint's
			// persistent worker, which completes it when the device produces
			// FRESH input (or replays the last payload on bInterval expiry).
			urbCtx, urbCancel := context.WithCancel(ctx)
			pendingMu.Lock()
			pending[seq] = urbCancel
			pendingMu.Unlock()
			jobs := inWorkers[ep]
			if jobs == nil {
				jobs = startInWorker(ep)
				inWorkers[ep] = jobs
			}
			jobs <- inJob{seq: seq, ctx: urbCtx, cancel: urbCancel}
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
		_ = xferFlags
		_ = devid
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
			// MS OS 1.0 probe: Windows queries STRING index 0xEE to discover
			// whether the device supports the Microsoft OS descriptor
			// protocol. Devices that opt in (currently Switch 2 Pro via
			// ns2pro) populate Descriptor.MicrosoftOS10 with a vendor code;
			// returning the fixed 18-byte signature here tells Windows it
			// can follow up with vendor-class requests for the Compatible-ID
			// and Extended-Properties descriptors (those follow-ups are not
			// wired yet — see MicrosoftOS10Descriptor doc).
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
	if breq == usbReqGetDescriptor && bm == usbReqTypeStandardToInterface {
		dtype := uint8(wValue >> 8)
		iface := uint8(wIndex & 0xff)
		var data []byte
		if int(iface) < len(desc.Interfaces) {
			ifaceConf := desc.Interfaces[iface]
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

	return nil
}

func (s *Server) buildConfigDescriptor(desc *usb.Descriptor) []byte {
	var b bytes.Buffer
	// Two ways to supply the config header: legacy `Config: ConfigHeader{...}`
	// (existing devices) and the slimmer `Configuration: &ConfigurationDescriptor{...}`
	// from ports like ns2pro. When the new form is set it wins — WTotalLength
	// and BNumInterfaces stay derived (we recompute them below anyway).
	var h usb.ConfigHeader
	if desc.Configuration != nil {
		h = usb.ConfigHeader{
			BConfigurationValue: desc.Configuration.BConfigurationValue,
			IConfiguration:      desc.Configuration.IConfiguration,
			BMAttributes:        desc.Configuration.BMAttributes,
			BMaxPower:           desc.Configuration.BMaxPower,
		}
	} else {
		h = desc.Config
	}
	h.WTotalLength = 0 // to be patched
	h.BNumInterfaces = interfaceCount(desc.Interfaces)
	if h.BConfigurationValue == 0 {
		h.BConfigurationValue = usbConfigValueDefault
	}
	if h.BMAttributes == 0 {
		h.BMAttributes = usbConfigAttrBusPowered
	}
	if h.BMaxPower == 0 {
		h.BMaxPower = usbConfigMaxPower100mA
	}
	h.Write(&b)
	for _, iface := range desc.Interfaces {
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
		}
	}

	data := b.Bytes()
	binary.LittleEndian.PutUint16(data[2:4], uint16(len(data)))
	return data
}

func interfaceCount(interfaces []usb.InterfaceConfig) uint8 {
	seen := make(map[uint8]struct{}, len(interfaces))
	for _, iface := range interfaces {
		seen[iface.Descriptor.BInterfaceNumber] = struct{}{}
	}
	return uint8(len(seen))
}

func usbipInterfaceDescs(interfaces []usb.InterfaceConfig) []usb.InterfaceConfig {
	out := make([]usb.InterfaceConfig, 0, len(interfaces))
	seen := make(map[uint8]struct{}, len(interfaces))
	for _, iface := range interfaces {
		n := iface.Descriptor.BInterfaceNumber
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, iface)
	}
	return out
}
