//go:build windows

package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"syscall"
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
	DIGCF_PRESENT         = 0x00000002
	DIGCF_DEVICEINTERFACE = 0x00000010
)

type SP_DEVICE_INTERFACE_DATA struct {
	CbSize             uint32
	InterfaceClassGuid windows.GUID
	Flags              uint32
	Reserved           uintptr
}

type SP_DEVICE_INTERFACE_DETAIL_DATA struct {
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

// PLUGIN_HARDWARE structure from usbip-win2.
//
// The driver validates Size against its own sizeof(plugin_hardware) and rejects a mismatch with
// ERROR_INSUFFICIENT_BUFFER, so this structure is version-specific. 0.9.7.8 appended a serial
// field, which makes the two layouts 1100 and 1116 bytes. Both are declared here and tried in turn
// rather than probing the installed version: the driver's own rejection is the authority on which
// one it wants, and it is unambiguous.
type attachIOCTL struct {
	Size       uint32
	PortOutput int32
	BusID      [32]byte
	Service    [niMaxServ]byte
	Host       [niMaxHost]byte
	Serial     [serialBufSz]byte
}

// Sizes of the two known plugin_hardware layouts, newest first.
//
// The legacy size deliberately is not unsafe.Offsetof(Serial) alone: the structure is 4-aligned, so
// the pre-0.9.7.8 layout ends on a padded boundary the raw offset would not give.
var attachIOCTLSizes = [2]uint32{
	uint32(unsafe.Sizeof(attachIOCTL{})),
	(uint32(unsafe.Offsetof(attachIOCTL{}.Serial)) + 3) &^ 3,
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
		"busID", deviceExportMeta.BusId,
		"deviceID", deviceExportMeta.DevId)

	if usbipServerPort == 0 {
		return 0, fmt.Errorf("ArgumentValidation: invalid TCP port number (0)")
	}

	devicePath, err := getDeviceInterfacePath(&deviceGUID)
	if err != nil {
		return 0, fmt.Errorf("Discovery: %w", err)
	}

	logger.Debug("Found usbip-win2 device", "path", devicePath)

	var ioctlData attachIOCTL

	busID := fmt.Sprintf("%d-%d", deviceExportMeta.BusId, deviceExportMeta.DevId)
	if len(busID) >= len(ioctlData.BusID) {
		return 0, fmt.Errorf("ArgumentValidation: bus ID too long: %s", busID)
	}
	copy(ioctlData.BusID[:], busID)

	service := fmt.Sprintf("%d", usbipServerPort)
	if len(service) >= len(ioctlData.Service) {
		return 0, fmt.Errorf("ArgumentValidation: service string too long: %s", service)
	}
	copy(ioctlData.Service[:], service)
	copy(ioctlData.Host[:], "localhost")

	devicePathUTF16, err := windows.UTF16PtrFromString(devicePath)
	if err != nil {
		return 0, fmt.Errorf("Open: failed to convert device path: %w", err)
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
		return 0, fmt.Errorf("Open: failed to open usbip-win2 device: %w", err)
	}
	defer windows.CloseHandle(handle)

	logger.Debug("Opened device handle")

	// Try each known plugin_hardware layout, newest first. A driver that expects the other one
	// rejects this cleanly and specifically with ERROR_INSUFFICIENT_BUFFER before doing anything,
	// so retrying is safe: it is a size negotiation, not a repeated attach attempt. Any other
	// failure is a real one and stops here rather than being retried against a layout the driver
	// has already shown it does not want.
	var bytesReturned uint32
	for index, size := range attachIOCTLSizes {
		ioctlData.Size = size
		ioctlData.PortOutput = 0
		err = windows.DeviceIoControl(
			handle,
			ioctlPluginHardware,
			(*byte)(unsafe.Pointer(&ioctlData)),
			size,
			(*byte)(unsafe.Pointer(&ioctlData)),
			size,
			&bytesReturned,
			nil,
		)
		if err == nil {
			break
		}

		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || index == len(attachIOCTLSizes)-1 {
			return 0, fmt.Errorf("IOControl: DeviceIoControl failed (plugin_hardware size %d): %w", size, err)
		}

		logger.Debug("Driver rejected the plugin_hardware layout; trying the other one", "size", size)
	}

	logger.Debug("IOCTL completed", "bytesReturned", bytesReturned, "portOutput", ioctlData.PortOutput)

	if ioctlData.PortOutput <= 0 {
		return 0, fmt.Errorf("ResponseValidation: invalid USB port returned: %d", ioctlData.PortOutput)
	}

	logger.Info("Successfully attached device via IOCTL",
		"busID", deviceExportMeta.BusId,
		"deviceID", deviceExportMeta.DevId,
		"usbPort", ioctlData.PortOutput)

	return int(ioctlData.PortOutput), nil
}

func attachViaCommand(ctx context.Context, deviceExportMeta *usbip.ExportMeta, usbipServerPort uint16, logger *slog.Logger) (int, error) {
	logger.Info("Auto-attaching localhost client", "busID", deviceExportMeta.BusId, "deviceID", deviceExportMeta.DevId)

	cmd := exec.CommandContext(
		ctx,
		"usbip",
		"--tcp-port",
		strconv.FormatUint(uint64(usbipServerPort), 10),
		"attach",
		"-r", "localhost",
		"-b", fmt.Sprintf("%d-%d", deviceExportMeta.BusId, deviceExportMeta.DevId),
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
		return fmt.Errorf("Open: failed to convert device path: %w", err)
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
		return fmt.Errorf("Open: failed to open usbip-win2 device: %w", err)
	}
	defer windows.CloseHandle(handle)

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
		uintptr(DIGCF_PRESENT|DIGCF_DEVICEINTERFACE))

	devInfo := windows.Handle(r0)
	if devInfo == windows.InvalidHandle {
		if e1 != 0 {
			return "", fmt.Errorf("Discovery: SetupDiGetClassDevsW failed: %w", e1)
		}
		return "", fmt.Errorf("Discovery: SetupDiGetClassDevsW failed with invalid handle")
	}
	defer func() {
		syscall.SyscallN(procSetupDiDestroyDeviceInfoList.Addr(), uintptr(devInfo))
	}()

	var interfaceData SP_DEVICE_INTERFACE_DATA
	interfaceData.CbSize = uint32(unsafe.Sizeof(interfaceData))

	r1, _, e2 := syscall.SyscallN(procSetupDiEnumDeviceInterfaces.Addr(),
		uintptr(devInfo),
		0,
		uintptr(unsafe.Pointer(guid)),
		0,
		uintptr(unsafe.Pointer(&interfaceData)))

	if r1 == 0 {
		if e2 != 0 {
			return "", fmt.Errorf("Discovery: usbip-win2 driver not found: %w", e2)
		}
		return "", fmt.Errorf("Discovery: usbip-win2 driver not found")
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
		return "", fmt.Errorf("Discovery: SetupDiGetDeviceInterfaceDetailW (size query) failed: %w", eSize)
	}
	if requiredSize == 0 {
		return "", fmt.Errorf("Discovery: SetupDiGetDeviceInterfaceDetailW (size query) returned no size")
	}

	detailData := make([]byte, requiredSize)
	detailHeader := (*SP_DEVICE_INTERFACE_DETAIL_DATA)(unsafe.Pointer(&detailData[0]))
	detailHeader.CbSize = uint32(unsafe.Sizeof(SP_DEVICE_INTERFACE_DETAIL_DATA{}))

	r2, _, e3 := syscall.SyscallN(procSetupDiGetDeviceInterfaceDetailW.Addr(),
		uintptr(devInfo),
		uintptr(unsafe.Pointer(&interfaceData)),
		uintptr(unsafe.Pointer(detailHeader)),
		uintptr(requiredSize),
		0,
		0)

	if r2 == 0 {
		if e3 != 0 {
			return "", fmt.Errorf("Discovery: SetupDiGetDeviceInterfaceDetailW failed: %w", e3)
		}
		return "", fmt.Errorf("Discovery: SetupDiGetDeviceInterfaceDetailW failed")
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
