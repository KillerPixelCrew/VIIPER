//go:build windows

package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Alia5/VIIPER/usbip"
	"golang.org/x/sys/windows"
)

var (
	setupapi                             = windows.NewLazySystemDLL("setupapi.dll")
	procSetupDiGetClassDevsW             = setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

const (
	DigcfPresent         = 0x00000002
	DigcfDeviceInterface = 0x00000010
)

type SpDeviceInterfaceData struct {
	CbSize             uint32
	InterfaceClassGUID windows.GUID
	Flags              uint32
	Reserved           uintptr
}

type SpDeviceInterfaceDetailData struct {
	CbSize     uint32
	DevicePath [1]uint16
}

// Device GUID from usbip-win2 driver
var deviceGUID = windows.GUID{
	Data1: 0xB4030C06,
	Data2: 0xDC5F,
	Data3: 0x4FCC,
	Data4: [8]byte{0x87, 0xEB, 0xE5, 0x51, 0x5A, 0x09, 0x35, 0xC0},
}

const (
	niMaxHost = 1025
	niMaxServ = 32
	// SERIAL_BUFSZ, appended to usbip::vhci::ioctl::plugin_hardware in usbip-win2 0.9.7.8.
	serialBufSz = 16
)

// PLUGIN_HARDWARE structure from usbip-win2 0.9.8.0 and later.
//
// In C++ it is `struct plugin_hardware : base, imported_device_location` plus trailing fields, so
// the imported_device_location base is padded to 1096 bytes on its own before the serial starts.
// The explicit padding here reproduces that layout; flattening the fields would put Serial and
// WskEvents 3 bytes early.
//
// The driver validates Size against its own sizeof(plugin_hardware) and rejects a mismatch before
// acting, so this structure is version-specific:
//
//   - 0.9.7.7 and earlier: 1100 bytes, ending after the padded location.
//   - 0.9.7.8: 1116 bytes, adding Serial.
//   - 0.9.8.0: 1120 bytes, adding WskEvents (the low-latency receive mode).
//
// All three are tried in turn, newest first, rather than probing the installed version: the
// driver's own rejection is the authority on which one it wants, and it is unambiguous.
type attachIOCTL struct {
	Size       uint32
	PortOutput int32
	BusID      [32]byte
	Service    [niMaxServ]byte
	Host       [niMaxHost]byte
	_          [3]byte
	Serial     [serialBufSz]byte
	// WskEvents selects the driver's WSK event-callback receive path, which usbip-win2 recommends
	// for devices that send small reports at a high rate, as every controller here does.
	WskEvents bool
	_         [3]byte
}

// The layout must match the driver byte for byte; these fail to compile if it drifts.
var (
	_ [unsafe.Sizeof(attachIOCTL{}) - 1120]struct{}
	_ [1120 - unsafe.Sizeof(attachIOCTL{})]struct{}
	_ [unsafe.Offsetof(attachIOCTL{}.Serial) - 1100]struct{}
	_ [1100 - unsafe.Offsetof(attachIOCTL{}.Serial)]struct{}
	_ [unsafe.Offsetof(attachIOCTL{}.WskEvents) - 1116]struct{}
	_ [1116 - unsafe.Offsetof(attachIOCTL{}.WskEvents)]struct{}
)

// Sizes of the known plugin_hardware layouts, newest first. An older driver reads only the prefix
// its size covers, so one buffer serves all three.
var attachIOCTLSizes = [3]uint32{
	uint32(unsafe.Sizeof(attachIOCTL{})),
	uint32(unsafe.Offsetof(attachIOCTL{}.WskEvents)),
	uint32(unsafe.Offsetof(attachIOCTL{}.Serial)),
}

// isLayoutRejection reports whether err is the driver refusing the plugin_hardware size. 0.9.7.x
// answers ERROR_INSUFFICIENT_BUFFER; 0.9.8.0 answers STATUS_INVALID_BUFFER_SIZE, which reaches
// user mode as ERROR_INVALID_USER_BUFFER. Both are returned before the driver does anything.
func isLayoutRejection(err error) bool {
	return errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || errors.Is(err, windows.ERROR_INVALID_USER_BUFFER)
}

const (
	fileDeviceUnknown    = 0x00000022
	methodBuffered       = 0
	fileReadData         = 0x0001
	fileWriteData        = 0x0002
	ioctlPluginHardware  = (fileDeviceUnknown << 16) | ((fileReadData | fileWriteData) << 14) | (0x800 << 2) | methodBuffered
	ioctlPlugoutHardware = (fileDeviceUnknown << 16) | ((fileReadData | fileWriteData) << 14) | (0x801 << 2) | methodBuffered
)

type plugoutHardware struct {
	Size uint32
	Port int32
}

func attachLocalhostClientImpl(ctx context.Context, deviceExportMeta *usbip.ExportMeta, usbipServerPort uint16, useNativeIOCTL bool, logger *slog.Logger) (int, error) {
	if useNativeIOCTL {
		return attachViaIOCTL(ctx, deviceExportMeta, usbipServerPort, logger)
	}
	return attachViaCommand(ctx, deviceExportMeta, usbipServerPort, logger)
}

func attachViaIOCTL(ctx context.Context, deviceExportMeta *usbip.ExportMeta, usbipServerPort uint16, logger *slog.Logger) (int, error) {
	logger.Info("Auto-attaching localhost client via native IOCTL",
		"busID", deviceExportMeta.BusID,
		"deviceID", deviceExportMeta.DevID)

	if usbipServerPort == 0 {
		return 0, fmt.Errorf("argumentValidation: invalid TCP port number (0)")
	}

	devicePath, err := getDeviceInterfacePath(&deviceGUID)
	if err != nil {
		return 0, fmt.Errorf("discovery: %w", err)
	}

	logger.Debug("Found usbip-win2 device", "path", devicePath)

	// Heap-allocated on purpose: with overlapped I/O the driver may still write into it after
	// this function returns, and a goroutine stack can move.
	ioctlData := new(attachIOCTL)
	ioctlData.WskEvents = true

	busID := fmt.Sprintf("%d-%d", deviceExportMeta.BusID, deviceExportMeta.DevID)
	if len(busID) >= len(ioctlData.BusID) {
		return 0, fmt.Errorf("argumentValidation: bus ID too long: %s", busID)
	}
	copy(ioctlData.BusID[:], busID)

	service := fmt.Sprintf("%d", usbipServerPort)
	if len(service) >= len(ioctlData.Service) {
		return 0, fmt.Errorf("argumentValidation: service string too long: %s", service)
	}
	copy(ioctlData.Service[:], service)
	copy(ioctlData.Host[:], "localhost")

	devicePathUTF16, err := windows.UTF16PtrFromString(devicePath)
	if err != nil {
		return 0, fmt.Errorf("open: failed to convert device path: %w", err)
	}

	// Overlapped, so the request honors ctx: a synchronous DeviceIoControl cannot be interrupted
	// once issued, and a wedged driver held the caller forever.
	handle, err := windows.CreateFile(
		devicePathUTF16,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		return 0, fmt.Errorf("open: failed to open usbip-win2 device: %w", err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // cleanup; nothing to do on failure

	logger.Debug("Opened device handle")

	// Try each known plugin_hardware layout, newest first. A driver that expects another one
	// rejects this cleanly and specifically before doing anything (see isLayoutRejection), so
	// retrying is safe: it is a size negotiation, not a repeated attach attempt. Any other failure
	// is a real one and stops here rather than being retried against a layout the driver has
	// already shown it does not want.
	var bytesReturned uint32
	for index, size := range attachIOCTLSizes {
		ioctlData.Size = size
		ioctlData.PortOutput = 0
		bytesReturned, err = pluginHardware(ctx, handle, ioctlData, size)
		if err == nil {
			break
		}
		if errors.Is(err, ErrAttachUncertain) {
			return 0, err
		}

		if !isLayoutRejection(err) || index == len(attachIOCTLSizes)-1 {
			return 0, fmt.Errorf("IOControl: DeviceIoControl failed (plugin_hardware size %d): %w", size, err)
		}

		logger.Debug("Driver rejected the plugin_hardware layout; trying an older one", "size", size)
	}

	logger.Debug("IOCTL completed", "bytesReturned", bytesReturned, "portOutput", ioctlData.PortOutput)

	if ioctlData.PortOutput <= 0 {
		// The driver accepted the request, so the device may be plugged in without a port
		// this side can detach by.
		return 0, fmt.Errorf("%w: responseValidation: invalid USB port returned: %d", ErrAttachUncertain, ioctlData.PortOutput)
	}

	logger.Info("Successfully attached device via IOCTL",
		"busID", deviceExportMeta.BusID,
		"deviceID", deviceExportMeta.DevID,
		"usbPort", ioctlData.PortOutput)

	return int(ioctlData.PortOutput), nil
}

// ioctlCancelGrace is how long a cancelled plugin_hardware request may take to
// complete before it is abandoned.
const ioctlCancelGrace = 5 * time.Second

// abandonedIOCTLs keeps everything a request that did not complete after
// cancellation still refers to reachable. The I/O manager copies its output and
// signals its event whenever the driver finally completes it.
var (
	abandonedMu     sync.Mutex
	abandonedIOCTLs []any
)

// pluginHardware issues plugin_hardware as overlapped I/O and waits for it
// under ctx. When ctx ends first the request is cancelled; a request that still
// has not completed after ioctlCancelGrace is abandoned. Any outcome after a
// cancellation other than success is ErrAttachUncertain, because the driver may
// have plugged the device in before it saw the cancellation.
func pluginHardware(ctx context.Context, handle windows.Handle, data *attachIOCTL, size uint32) (uint32, error) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, fmt.Errorf("IOControl: CreateEvent failed: %w", err)
	}
	overlapped := &windows.Overlapped{HEvent: event}
	var returned uint32
	err = windows.DeviceIoControl(
		handle,
		ioctlPluginHardware,
		(*byte)(unsafe.Pointer(data)),
		size,
		(*byte)(unsafe.Pointer(data)),
		size,
		&returned,
		overlapped,
	)
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		windows.CloseHandle(event) //nolint:errcheck // cleanup; nothing to do on failure
		return returned, err
	}

	completed := waitEvent(ctx, event)
	if !completed {
		_ = windows.CancelIoEx(handle, overlapped)
		if r, _ := windows.WaitForSingleObject(event, uint32(ioctlCancelGrace/time.Millisecond)); r != windows.WAIT_OBJECT_0 {
			abandonedMu.Lock()
			abandonedIOCTLs = append(abandonedIOCTLs, event, overlapped, data)
			abandonedMu.Unlock()
			return 0, fmt.Errorf("%w: IOControl: plugin_hardware did not complete after cancellation: %v", ErrAttachUncertain, ctx.Err())
		}
	}
	err = windows.GetOverlappedResult(handle, overlapped, &returned, false)
	windows.CloseHandle(event) //nolint:errcheck // cleanup; nothing to do on failure
	if !completed && err != nil {
		return 0, fmt.Errorf("%w: IOControl: plugin_hardware cancelled: %v (%v)", ErrAttachUncertain, ctx.Err(), err)
	}
	return returned, err
}

// waitEvent waits for event, returning false as soon as ctx ends first.
func waitEvent(ctx context.Context, event windows.Handle) bool {
	for {
		if r, _ := windows.WaitForSingleObject(event, 50); r == windows.WAIT_OBJECT_0 {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}
}

func attachViaCommand(ctx context.Context, deviceExportMeta *usbip.ExportMeta, usbipServerPort uint16, logger *slog.Logger) (int, error) {
	logger.Info("Auto-attaching localhost client", "busID", deviceExportMeta.BusID, "deviceID", deviceExportMeta.DevID)

	cmd := exec.CommandContext(
		ctx,
		"usbip",
		"--tcp-port",
		strconv.FormatUint(uint64(usbipServerPort), 10),
		"attach",
		"-r", "localhost",
		"-b", fmt.Sprintf("%d-%d", deviceExportMeta.BusID, deviceExportMeta.DevID),
		"-t",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("Failed to attach device",
			"error", err,
			"port", usbipServerPort,
			"output", string(output))
		return 0, err
	}
	logger.Debug("usbip attach output", "output", string(output))

	port, err := parseAttachedPort(output)
	if err != nil {
		return 0, fmt.Errorf("parse usbip attach port: %w", err)
	}
	return port, nil
}

func detachLocalhostClientImpl(ctx context.Context, port int, logger *slog.Logger) error {
	if err := detachViaIOCTL(port, logger); err == nil {
		return nil
	} else {
		logger.Debug("Native IOCTL detach failed, trying usbip executable", "error", err)
	}

	output, err := exec.CommandContext(ctx, "usbip", "detach", "-p", strconv.Itoa(port)).CombinedOutput()
	if err != nil {
		logger.Error("Failed to detach device", "error", err, "port", port, "output", string(output))
		return err
	}
	logger.Debug("usbip detach output", "output", string(output), "port", port)
	return nil
}

func detachViaIOCTL(port int, logger *slog.Logger) error {
	devicePath, err := getDeviceInterfacePath(&deviceGUID)
	if err != nil {
		return err
	}

	devicePathUTF16, err := windows.UTF16PtrFromString(devicePath)
	if err != nil {
		return fmt.Errorf("open: failed to convert device path: %w", err)
	}

	handle, err := windows.CreateFile(
		devicePathUTF16,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return fmt.Errorf("open: failed to open usbip-win2 device: %w", err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // cleanup; nothing to do on failure

	data := plugoutHardware{Size: uint32(unsafe.Sizeof(plugoutHardware{})), Port: int32(port)}
	var bytesReturned uint32
	if err := windows.DeviceIoControl(
		handle,
		ioctlPlugoutHardware,
		(*byte)(unsafe.Pointer(&data)),
		uint32(unsafe.Sizeof(data)),
		nil,
		0,
		&bytesReturned,
		nil,
	); err != nil {
		return fmt.Errorf("IOControl: DeviceIoControl failed: %w", err)
	}

	logger.Debug("Successfully detached device via IOCTL", "port", port)
	return nil
}

func getDeviceInterfacePath(guid *windows.GUID) (string, error) {
	r0, _, e1 := syscall.SyscallN(procSetupDiGetClassDevsW.Addr(),
		uintptr(unsafe.Pointer(guid)),
		0,
		0,
		uintptr(DigcfPresent|DigcfDeviceInterface))

	devInfo := windows.Handle(r0)
	if devInfo == windows.InvalidHandle {
		if e1 != 0 {
			return "", fmt.Errorf("discovery: SetupDiGetClassDevsW failed: %w", e1)
		}
		return "", fmt.Errorf("discovery: SetupDiGetClassDevsW failed with invalid handle")
	}
	defer func() {
		syscall.SyscallN(procSetupDiDestroyDeviceInfoList.Addr(), uintptr(devInfo)) //nolint:errcheck // cleanup; nothing to do on failure
	}()

	var interfaceData SpDeviceInterfaceData
	interfaceData.CbSize = uint32(unsafe.Sizeof(interfaceData))

	r1, _, e2 := syscall.SyscallN(procSetupDiEnumDeviceInterfaces.Addr(),
		uintptr(devInfo),
		0,
		uintptr(unsafe.Pointer(guid)),
		0,
		uintptr(unsafe.Pointer(&interfaceData)))

	if r1 == 0 {
		if e2 != 0 {
			return "", fmt.Errorf("discovery: usbip-win2 driver not found: %w", e2)
		}
		return "", fmt.Errorf("discovery: usbip-win2 driver not found")
	}

	var requiredSize uint32
	rSize, _, eSize := syscall.SyscallN(procSetupDiGetDeviceInterfaceDetailW.Addr(),
		uintptr(devInfo),
		uintptr(unsafe.Pointer(&interfaceData)),
		0,
		0,
		uintptr(unsafe.Pointer(&requiredSize)),
		0)

	// The size query is expected to fail with ERROR_INSUFFICIENT_BUFFER; that is
	// how it reports the size. Any other failure means requiredSize was not
	// written, and indexing a zero-length slice below would panic instead of
	// reporting why discovery failed.
	if rSize == 0 && eSize != windows.ERROR_INSUFFICIENT_BUFFER {
		return "", fmt.Errorf("discovery: SetupDiGetDeviceInterfaceDetailW (size query) failed: %w", eSize)
	}
	if requiredSize == 0 {
		return "", fmt.Errorf("discovery: SetupDiGetDeviceInterfaceDetailW (size query) returned no size")
	}

	detailData := make([]byte, requiredSize)
	detailHeader := (*SpDeviceInterfaceDetailData)(unsafe.Pointer(&detailData[0]))
	detailHeader.CbSize = uint32(unsafe.Sizeof(SpDeviceInterfaceDetailData{}))

	r2, _, e3 := syscall.SyscallN(procSetupDiGetDeviceInterfaceDetailW.Addr(),
		uintptr(devInfo),
		uintptr(unsafe.Pointer(&interfaceData)),
		uintptr(unsafe.Pointer(detailHeader)),
		uintptr(requiredSize),
		0,
		0)

	if r2 == 0 {
		if e3 != 0 {
			return "", fmt.Errorf("discovery: SetupDiGetDeviceInterfaceDetailW failed: %w", e3)
		}
		return "", fmt.Errorf("discovery: SetupDiGetDeviceInterfaceDetailW failed")
	}

	path := windows.UTF16PtrToString(&detailHeader.DevicePath[0])
	return path, nil
}

func CheckAutoAttachPrerequisites(useNativeIOCTL bool, logger *slog.Logger) bool {
	if useNativeIOCTL {
		_, err := getDeviceInterfacePath(&deviceGUID)
		if err != nil {
			logger.Warn("Native IOCTL auto-attach prerequisites not met", "error", err)
			logger.Warn("Native IOCTL auto-attach is unavailable until discovery succeeds")
			logger.Info("If usbip-win2 is not installed, download and install:")
			logger.Info("  https://github.com/vadimgrn/usbip-win2")
			logger.Info("  https://github.com/OSSign/vadimgrn--usbip-win2")
			return false
		}
		logger.Debug("usbip-win2 driver found")
		return true
	}

	if _, err := exec.LookPath("usbip.exe"); err != nil {
		logger.Warn("USB/IP tool 'usbip.exe' not found in PATH")
		logger.Warn("Auto-attach requires usbip-win2")
		logger.Info("Download and install usbip-win2:")
		logger.Info("  https://github.com/vadimgrn/usbip-win2")
		return false
	}

	logger.Debug("usbip.exe tool found in PATH")
	return true
}
