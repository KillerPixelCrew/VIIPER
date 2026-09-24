// Package main implements a CGo shared library (c-shared) that exposes
// VIIPER's virtual USB device functionality through a flat C API.
//
// Build:
//
//	GOOS=windows GOARCH=amd64 CGO_ENABLED=1 go build -buildmode=c-shared -o libviiper.dll ./clib/
//	GOOS=linux   GOARCH=amd64 CGO_ENABLED=1 go build -buildmode=c-shared -o libviiper.so  ./clib/
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef void (*viiper_feedback_fn)(uint32_t bus_id, uint32_t device_id, const uint8_t* data, int len, void* user_data);

// Bridge function to call the feedback callback from Go.
static inline void bridge_feedback_fn(viiper_feedback_fn fn, uint32_t bus_id, uint32_t device_id, const uint8_t* data, int len, void* user_data) {
	fn(bus_id, device_id, data, len, user_data);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	// Blank-import the registry package to trigger all device init() registrations.
	_ "github.com/Alia5/VIIPER/internal/registry"

	"github.com/Alia5/VIIPER/device"
	"github.com/Alia5/VIIPER/device/dualsense"
	"github.com/Alia5/VIIPER/device/dualshock4"
	"github.com/Alia5/VIIPER/device/ns2pro"
	"github.com/Alia5/VIIPER/device/steamcontroller"
	"github.com/Alia5/VIIPER/device/steamdeck"
	"github.com/Alia5/VIIPER/device/switchpro"
	"github.com/Alia5/VIIPER/device/xbox360"
	"github.com/Alia5/VIIPER/device/xboxelite2"
	"github.com/Alia5/VIIPER/device/xboxgip"
	elite2state "github.com/Alia5/VIIPER/internal/inputstate/elite2"
	"github.com/Alia5/VIIPER/internal/server/api"
	usbsrv "github.com/Alia5/VIIPER/internal/server/usb"
	"github.com/Alia5/VIIPER/usb"
	"github.com/Alia5/VIIPER/usbip"
	"github.com/Alia5/VIIPER/virtualbus"
)

// ---------------------------------------------------------------------------
// Global state
// ---------------------------------------------------------------------------

// Set by eng/build-viiper.ps1 through -ldflags -X, so a staged library names the
// VIIPER commit it was built from.
var (
	buildRevision = "unknown"
	buildModified = "unknown"
)

// attachTimeout bounds each driver attempt in viiper_device_attach.
const attachTimeout = 15 * time.Second

var (
	mu     sync.Mutex
	server *usbsrv.Server

	// errorMu guards lastError alone, so an entry point can record its error after releasing mu
	// (viiper_device_remove does) without racing viiper_last_error.
	errorMu   sync.Mutex
	lastError string

	// devices tracks metadata for each device we've added.
	devices = make(map[deviceKey]*deviceInfo)

	// feedbackCallbacks stores registered C callbacks for devices.
	feedbackCallbacks = make(map[deviceKey]*feedbackReg)
)

type deviceKey struct {
	busID uint32
	devID uint32
}

type deviceInfo struct {
	dev      usb.Device
	typeName string // resolved registry name, e.g. "xbox360", "dualshock4", "dualsenseedge", "xboxelite2", "steamcontroller"
	port     int
	// attaching and removing are guarded by mu. The attach and remove paths both release mu
	// around their driver operation, and these keep them from interleaving on one device.
	attaching bool
	removing  bool
}

// deviceAlias describes a user-friendly device-type name and how it maps
// onto a registry entry, an optional profile string, and optional VID/PID
// overrides. The overrides are only applied when the caller does not pass
// explicit VID/PID values via viiper_device_add_ex.
type deviceAlias struct {
	registryName   string
	profile        string
	vidOverride    *uint16
	pidOverride    *uint16
	deprecationMsg string // logged once-per-process via warnDeprecatedAlias when this alias is used.
}

func u16Ptr(v uint16) *uint16 { return &v }

// Handheld PIDs in Valve's 0x12Fx range used by Steam Input on
// third-party handhelds. The 0x28DE VID is Valve Corporation.
var (
	pidValveVendor   = u16Ptr(0x28DE)
	pidSteamDeck     = u16Ptr(0x1205)
	pidMSIClaw       = u16Ptr(0x12FA)
	pidLenovoLegion2 = u16Ptr(0x12FB)
	pidZotacZone     = u16Ptr(0x12FC)
	pidASUSRogAlly   = u16Ptr(0x12FD)
	pidLenovoLegion  = u16Ptr(0x12FE)
	pidLenovoLegionS = u16Ptr(0x12FF)
)

// deviceTypeAliases maps user-friendly names to their registry type + profile.
// If a name is not in this map, it's used as-is against the device registry.
var deviceTypeAliases = map[string]deviceAlias{
	// Steam Deck and Valve 0x12Fx-range third-party handhelds. All route to
	// the steamdeck device with a VID/PID override per platform.
	"steamdeck":   {registryName: "steamdeck"},
	"steam-deck":  {registryName: "steamdeck"},
	"deck":        {registryName: "steamdeck"},
	"msi-claw":    {registryName: "steamdeck", vidOverride: pidValveVendor, pidOverride: pidMSIClaw},
	"legion-go":   {registryName: "steamdeck", vidOverride: pidValveVendor, pidOverride: pidLenovoLegion},
	"legion-go-2": {registryName: "steamdeck", vidOverride: pidValveVendor, pidOverride: pidLenovoLegion2},
	"legion-go-s": {registryName: "steamdeck", vidOverride: pidValveVendor, pidOverride: pidLenovoLegionS},
	"rog-ally":    {registryName: "steamdeck", vidOverride: pidValveVendor, pidOverride: pidASUSRogAlly},
	"zotac-zone":  {registryName: "steamdeck", vidOverride: pidValveVendor, pidOverride: pidZotacZone},

	// Deprecated steamdeck-family names. Pre-rewrite these routed to the broken
	// device/steamcontroller profile; they now resolve to the real steamdeck.
	"steamdeck-generic": {registryName: "steamdeck", deprecationMsg: "'steamdeck-generic' is deprecated; use 'steamdeck' (or a specific handheld alias like 'legion-go-2')"},
	"steam-generic":     {registryName: "steamdeck", deprecationMsg: "'steam-generic' is deprecated; use 'steamdeck' (or a specific handheld alias like 'legion-go-2')"},

	// Steam Controller V1 (wired Gordon). Canonical name: "gordon".
	"gordon":              {registryName: "steamcontroller"},
	"steam-controller-v1": {registryName: "steamcontroller"},
	"steam-controller":    {registryName: "steamcontroller", deprecationMsg: "'steam-controller' now refers to the wired Steam Controller V1 (Gordon); use 'steamdeck' for the Steam Deck handheld"},
	"steamcontroller-v1":  {registryName: "steamcontroller"},

	// Xbox family.
	"xbox-one":   {registryName: "xboxelite2", profile: "xbox-one"},
	"xbox-elite": {registryName: "xboxelite2", profile: "xbox-one-elite"},

	// Switch family.
	"joycon-left":  {registryName: "switchpro", profile: "joycon-left"},
	"joycon-right": {registryName: "switchpro", profile: "joycon-right"},

	// Sony DualSense (regular, not Edge).
	"dualsense": {registryName: "dualsense"},
	"ds5":       {registryName: "dualsense"},
}

// deprecationOnce ensures each deprecation warning fires at most once per
// process lifetime, regardless of how many times the alias is used.
var deprecationOnce sync.Map // map[string]*sync.Once

func warnDeprecatedAlias(name, msg string) {
	once, _ := deprecationOnce.LoadOrStore(name, &sync.Once{})
	once.(*sync.Once).Do(func() { slog.Warn(msg, "alias", name) })
}

// applyAlias resolves a user-typed device-type name through deviceTypeAliases
// and populates registryName + opts (profile and any VID/PID overrides not
// already set by the caller).
func applyAlias(tn string, opts *device.CreateOptions) string {
	alias, ok := deviceTypeAliases[tn]
	if !ok {
		return tn
	}
	if alias.profile != "" {
		opts.DeviceSpecific = fmt.Sprintf(`{"profile":%q}`, alias.profile)
	}
	if alias.vidOverride != nil && opts.IDVendor == nil {
		v := *alias.vidOverride
		opts.IDVendor = &v
	}
	if alias.pidOverride != nil && opts.IDProduct == nil {
		p := *alias.pidOverride
		opts.IDProduct = &p
	}
	if alias.deprecationMsg != "" {
		warnDeprecatedAlias(tn, alias.deprecationMsg)
	}
	return alias.registryName
}

// hiddenDeviceTypes are device types from the registry that should not appear
// in the user-facing device type list (non-controller devices).
var hiddenDeviceTypes = map[string]bool{
	"keyboard": true,
	"mouse":    true,
	"xboxgip":  true,
}

type feedbackReg struct {
	fn       C.viiper_feedback_fn
	userData unsafe.Pointer
	inflight sync.WaitGroup
}

// ---------------------------------------------------------------------------
// Error helpers
// ---------------------------------------------------------------------------

// recoverExport turns a Go panic in an exported call into an error return.
// Unrecovered, a panic on a cgo callback frame aborts the whole host process,
// which no managed caller can catch. It must be the first deferred call, so it
// runs after the function's own deferred unlocks. result may be nil for calls
// with no status to report.
func recoverExport(result *C.int) {
	r := recover()
	if r == nil {
		return
	}
	slog.Error("recovered panic in exported call", "panic", r, "stack", string(debug.Stack()))
	if result != nil {
		*result = setError(fmt.Errorf("internal error: %v", r))
	}
}

// recoverGoroutine logs a panic in a background goroutine instead of letting it
// abort the host process.
func recoverGoroutine(where string) {
	if r := recover(); r != nil {
		slog.Error("recovered panic", "where", where, "panic", r, "stack", string(debug.Stack()))
	}
}

func setError(err error) C.int {
	errorMu.Lock()
	defer errorMu.Unlock()
	if err != nil {
		lastError = err.Error()
		return -1
	}
	lastError = ""
	return 0
}

// viiper_free_string frees a C string previously returned by viiper_last_error
// or viiper_list_device_types. The caller must call this to avoid memory leaks.
//
//export viiper_free_string
func viiper_free_string(s *C.char) {
	if s != nil {
		C.free(unsafe.Pointer(s))
	}
}

//export viiper_last_error
func viiper_last_error() (out *C.char) {
	defer recoverExport(nil)
	errorMu.Lock()
	defer errorMu.Unlock()
	if lastError == "" {
		return nil
	}
	return C.CString(lastError)
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// CPU profiling hook: when VIIPER_CPUPROFILE is set, every init/shutdown
// cycle writes one pprof file "<value>.<n>" (n increments per cycle, so
// benchmark iterations don't overwrite each other).
var (
	cpuProfileFile  *os.File
	cpuProfileCount int
)

func maybeStartCPUProfile() {
	base := os.Getenv("VIIPER_CPUPROFILE")
	if base == "" {
		return
	}
	cpuProfileCount++
	f, err := os.Create(fmt.Sprintf("%s.%d", base, cpuProfileCount))
	if err != nil {
		slog.Error("cpuprofile: create failed", "error", err)
		return
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		slog.Error("cpuprofile: start failed", "error", err)
		_ = f.Close()
		return
	}
	cpuProfileFile = f
}

func maybeStopCPUProfile() {
	if cpuProfileFile == nil {
		return
	}
	pprof.StopCPUProfile()
	_ = cpuProfileFile.Close()
	cpuProfileFile = nil
}

// idleModeFromEnv resolves the idle-endpoint mode: VIIPER_IDLE_MODE
// (auto|nak|keepalive) wins; the older VIIPER_NAK_IDLE=1/0 is honored for
// compatibility; default is "auto" (per-device).
func idleModeFromEnv() string {
	if m := os.Getenv("VIIPER_IDLE_MODE"); m != "" {
		return m
	}
	switch os.Getenv("VIIPER_NAK_IDLE") {
	case "1":
		return "nak"
	case "0":
		return "keepalive"
	}
	return "auto"
}

//export viiper_init
func viiper_init(listenAddr *C.char) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()
	defer mu.Unlock()

	if server != nil {
		return setError(fmt.Errorf("already initialized"))
	}

	addr := C.GoString(listenAddr)
	if addr == "" {
		addr = "0.0.0.0:3241"
	}

	// The embedded server runs a handful of goroutines; on big machines the
	// default GOMAXPROCS=NumCPU only adds scheduler and netpoller overhead.
	// VIIPER_GOMAXPROCS overrides; GOGC/GOMEMLIMIT work as usual via env.
	if v := os.Getenv("VIIPER_GOMAXPROCS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			runtime.GOMAXPROCS(n)
		}
	} else if runtime.NumCPU() > 4 {
		runtime.GOMAXPROCS(4)
	}

	// Set up file-based logging next to the DLL for protocol debugging.
	if exe, err := os.Executable(); err == nil {
		logPath := filepath.Join(filepath.Dir(exe), "viiper_go_debug.log")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			handler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})
			slog.SetDefault(slog.New(handler))
		}
	}
	logger := slog.Default()
	logger.Info("libviiper starting", "revision", buildRevision, "modified", buildModified)

	cfg := usbsrv.ServerConfig{
		Addr:                    addr,
		ConnectionTimeout:       30 * time.Second,
		BusCleanupTimeout:       5 * time.Second,
		WriteBatchFlushInterval: 0, // immediate writes — data-driven completion makes batching counterproductive
		// Default ON (matches real-hardware poll pacing; confirmed cheaper in
		// EmulationBench). VIIPER_HW_PACED=0 restores data-driven completion.
		HardwarePacedCompletions: os.Getenv("VIIPER_HW_PACED") != "0",
		IdleMode:                 idleModeFromEnv(),
	}

	server = usbsrv.New(cfg, logger, nil)

	go func() {
		defer recoverGoroutine("USBIP server")
		if err := server.ListenAndServe(); err != nil {
			slog.Error("USBIP server error", "error", err)
		}
	}()

	// Wait for the server to be ready (listener bound). ListenAndServe closes Ready() on a bind
	// failure too, specifically so this cannot block forever holding mu; ReadyErr distinguishes
	// that case from an actual successful bind.
	<-server.Ready()
	if err := server.ReadyErr(); err != nil {
		server = nil
		return setError(fmt.Errorf("listen: %w", err))
	}

	maybeStartCPUProfile()

	return 0
}

//export viiper_shutdown
func viiper_shutdown() {
	defer recoverExport(nil)
	mu.Lock()
	current := server
	if current == nil {
		mu.Unlock()
		return
	}

	// Quiesce feedback the way viiper_device_remove does, before and after teardown. A callback
	// already past its registration check keeps running after its registration is removed, and
	// the caller frees the callback's user data as soon as this returns. The wait happens without
	// mu, because a callback may re-enter the API.
	pending := takeFeedbackCallbacksLocked()
	mu.Unlock()
	waitFeedbackCallbacks(pending)

	waitFeedbackCallbacks(tearDownServer(current))
}

// tearDownServer stops current and clears all device tracking, returning the
// feedback registrations made while the first drain waited, with server nil so
// no further registration can be made. It returns nil when another shutdown
// completed first.
func tearDownServer(current *usbsrv.Server) map[deviceKey]*feedbackReg {
	mu.Lock()
	defer mu.Unlock()
	if server != current {
		return nil
	}

	// Remove all buses (which removes all devices).
	for _, busID := range server.ListBuses() {
		_ = server.RemoveBus(busID)
	}

	_ = server.Close()
	server = nil

	maybeStopCPUProfile()

	devices = make(map[deviceKey]*deviceInfo)
	clearX360Handles()
	return takeFeedbackCallbacksLocked()
}

// takeFeedbackCallbacksLocked removes every feedback registration, so no new callback can start
// for any of them. Must be called with mu held.
func takeFeedbackCallbacksLocked() map[deviceKey]*feedbackReg {
	taken := feedbackCallbacks
	feedbackCallbacks = make(map[deviceKey]*feedbackReg)
	return taken
}

// waitFeedbackCallbacks waits for callbacks already in flight. Must be called without mu.
func waitFeedbackCallbacks(regs map[deviceKey]*feedbackReg) {
	for _, reg := range regs {
		reg.inflight.Wait()
	}
}

// ---------------------------------------------------------------------------
// Bus management
// ---------------------------------------------------------------------------

//export viiper_bus_create
func viiper_bus_create(busID C.uint32_t) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()
	defer mu.Unlock()

	if server == nil {
		return setError(fmt.Errorf("not initialized"))
	}

	bus, err := virtualbus.NewWithBusID(uint32(busID))
	if err != nil {
		return setError(err)
	}

	if err := server.AddBus(bus); err != nil {
		_ = bus.Close()
		return setError(err)
	}

	return 0
}

//export viiper_bus_remove
func viiper_bus_remove(busID C.uint32_t) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()
	defer mu.Unlock()

	if server == nil {
		return setError(fmt.Errorf("not initialized"))
	}

	bid := uint32(busID)

	// Clean up device and feedback tracking for this bus.
	for k := range devices {
		if k.busID == bid {
			delete(devices, k)
			delete(feedbackCallbacks, k)
		}
	}

	if err := server.RemoveBus(bid); err != nil {
		return setError(err)
	}

	return 0
}

// ---------------------------------------------------------------------------
// Device management
// ---------------------------------------------------------------------------

//export viiper_device_add
func viiper_device_add(busID C.uint32_t, typeName *C.char, outDeviceID *C.uint32_t) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()
	defer mu.Unlock()

	if server == nil {
		return setError(fmt.Errorf("not initialized"))
	}

	bid := uint32(busID)
	tn := strings.ToLower(C.GoString(typeName))

	// Resolve aliases — populates profile, VID/PID overrides, prints any
	// deprecation warning once-per-process.
	var opts device.CreateOptions
	registryName := applyAlias(tn, &opts)

	reg := api.GetRegistration(registryName)
	if reg == nil {
		return setError(fmt.Errorf("unknown device type: %s", tn))
	}

	dev, err := reg.CreateDevice(&opts)
	if err != nil {
		return setError(fmt.Errorf("create device: %w", err))
	}

	bus := server.GetBus(bid)
	if bus == nil {
		return setError(fmt.Errorf("bus %d not found", bid))
	}

	_, err = bus.Add(dev)
	if err != nil {
		return setError(fmt.Errorf("add device to bus: %w", err))
	}

	// Find the device ID that was assigned.
	metas := bus.GetAllDeviceMetas()
	var devID uint32
	for _, m := range metas {
		if m.Dev == dev {
			devID = m.Meta.DevID
			break
		}
	}

	// Track the device using the registry name for input dispatch.
	key := deviceKey{busID: bid, devID: devID}
	devices[key] = &deviceInfo{
		dev:      dev,
		typeName: registryName,
	}

	if outDeviceID != nil {
		*outDeviceID = C.uint32_t(devID)
	}

	// WSGM patch: viiper_device_add no longer attaches. Attaching is viiper_device_attach's job
	// and the caller's decision.
	//
	// Upstream attached here as well, which made the pair add+attach produce TWO usbip
	// attachments of the same device: two ports in `usbip port`, both pointing at the same
	// bus/dev, and two identical controllers in Steam. Attach was documented here only as a
	// retry ("the caller can retry with viiper_device_attach"), so a caller that follows the API
	// literally gets a duplicate rather than a retry.
	//
	// It also made the ordering unachievable. A caller cannot present a neutral first frame
	// before the host enumerates the device if adding it is what enumerates it: the host starts
	// polling during add, before any input has been submitted, and reads whatever the device
	// state happens to be. With attach explicit, the caller can open the fast handle, submit a
	// neutral frame, and only then let Windows see the device.

	return 0
}

// viiper_device_add_ex is like viiper_device_add but allows overriding the USB VID and PID.
// Pass 0 for vid or pid to use the default for the device type/profile.
//
//export viiper_device_add_ex
func viiper_device_add_ex(busID C.uint32_t, typeName *C.char, vid C.uint16_t, pid C.uint16_t, outDeviceID *C.uint32_t) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()
	defer mu.Unlock()

	if server == nil {
		return setError(fmt.Errorf("not initialized"))
	}

	bid := uint32(busID)
	tn := strings.ToLower(C.GoString(typeName))

	// Explicit _ex arguments win over alias VID/PID overrides — apply them
	// first so applyAlias's "only-set-if-unset" rule preserves them.
	var opts device.CreateOptions
	if vid != 0 {
		v := uint16(vid)
		opts.IDVendor = &v
	}
	if pid != 0 {
		p := uint16(pid)
		opts.IDProduct = &p
	}
	registryName := applyAlias(tn, &opts)

	reg := api.GetRegistration(registryName)
	if reg == nil {
		return setError(fmt.Errorf("unknown device type: %s", tn))
	}

	dev, err := reg.CreateDevice(&opts)
	if err != nil {
		return setError(fmt.Errorf("create device: %w", err))
	}

	bus := server.GetBus(bid)
	if bus == nil {
		return setError(fmt.Errorf("bus %d not found", bid))
	}

	_, err = bus.Add(dev)
	if err != nil {
		return setError(fmt.Errorf("add device to bus: %w", err))
	}

	metas := bus.GetAllDeviceMetas()
	var devID uint32
	for _, m := range metas {
		if m.Dev == dev {
			devID = m.Meta.DevID
			break
		}
	}

	key := deviceKey{busID: bid, devID: devID}
	devices[key] = &deviceInfo{
		dev:      dev,
		typeName: registryName,
	}

	if outDeviceID != nil {
		*outDeviceID = C.uint32_t(devID)
	}

	// viiper_device_add_ex does not attach either, for the same reasons
	// viiper_device_add stopped: add+attach produced two USB/IP attachments of
	// one device, and enumerating during add makes it impossible to present a
	// neutral first frame before the host starts polling.

	return 0
}

// viiper_device_attach explicitly attaches a device to Windows via the usbip-win2 client.
// This is called automatically by viiper_device_add, but can be called separately if needed.
//
//export viiper_device_attach
func viiper_device_attach(busID C.uint32_t, deviceID C.uint32_t) (rc C.int) {
	defer recoverExport(&rc)
	bid := uint32(busID)
	did := uint32(deviceID)
	key := deviceKey{busID: bid, devID: did}

	mu.Lock()
	if server == nil {
		mu.Unlock()
		return setError(fmt.Errorf("not initialized"))
	}
	current := server
	port := server.GetListenPort()
	if port == 0 {
		mu.Unlock()
		return setError(fmt.Errorf("server listen port not available"))
	}
	info := devices[key]
	if info != nil {
		if info.removing {
			mu.Unlock()
			return setError(fmt.Errorf("device %d-%d is being removed", bid, did))
		}
		if info.attaching {
			mu.Unlock()
			return setError(fmt.Errorf("device %d-%d is already being attached", bid, did))
		}
		info.attaching = true
	}
	mu.Unlock()

	// The driver operation runs without mu, as in viiper_device_remove. Holding it let a wedged
	// usbip-win2 driver or a stuck usbip.exe block every other exported call, viiper_shutdown
	// included, for as long as the attach took, which could be forever.
	exportMeta := &usbip.ExportMeta{
		BusId: bid,
		DevId: did,
	}
	logger := slog.Default()
	attachCtx, cancelAttach := context.WithTimeout(context.Background(), attachTimeout)
	defer cancelAttach()

	// Try native IOCTL first, then fall back to usbip.exe command, but only when the IOCTL
	// failure proves nothing was plugged in. An uncertain outcome retried through usbip.exe
	// produced two attachments of one device, of which removal could detach only one.
	attachedPort, err := api.AttachLocalhostClientWithPort(attachCtx, exportMeta, port, true, logger)
	if err != nil && !errors.Is(err, api.ErrAttachUncertain) {
		slog.Warn("attach via IOCTL failed, trying usbip.exe", "error", err)
		fallbackCtx, cancelFallback := context.WithTimeout(context.Background(), attachTimeout)
		defer cancelFallback()
		attachedPort, err = api.AttachLocalhostClientWithPort(fallbackCtx, exportMeta, port, false, logger)
	}

	mu.Lock()
	stale := server != current || devices[key] != info || (info != nil && info.removing)
	if info != nil {
		info.attaching = false
		if err == nil && !stale {
			info.port = attachedPort
		}
	}
	mu.Unlock()

	if err != nil {
		return setError(fmt.Errorf("attach device: %w", err))
	}
	if stale {
		// Removal or shutdown ran while this attach was in the driver and could not know the
		// port; do not leave the new attachment behind.
		detachCtx, cancelDetach := context.WithTimeout(context.Background(), attachTimeout)
		defer cancelDetach()
		if derr := api.DetachLocalhostClient(detachCtx, attachedPort, logger); derr != nil {
			slog.Warn("detach after a concurrent removal failed", "busID", bid, "deviceID", did, "port", attachedPort, "error", derr)
		}
		return setError(fmt.Errorf("device %d-%d was removed while attaching", bid, did))
	}
	return 0
}

//export viiper_device_remove
func viiper_device_remove(busID C.uint32_t, deviceID C.uint32_t) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()

	if server == nil {
		mu.Unlock()
		return setError(fmt.Errorf("not initialized"))
	}

	bid := uint32(busID)
	did := uint32(deviceID)

	key := deviceKey{busID: bid, devID: did}
	info := devices[key]
	if info == nil {
		mu.Unlock()
		return setError(fmt.Errorf("device %d-%d not found", bid, did))
	}

	// Stop reverse calls before unplugging the client. usbip-win2 can deliver one
	// last output packet while plugout is in progress; its device callback must not
	// re-enter a caller that is synchronously waiting for this removal to return.
	if info.removing {
		mu.Unlock()
		return setError(fmt.Errorf("device %d-%d is already being removed", bid, did))
	}
	// An attach in flight sees this flag when it returns and detaches its own new attachment,
	// because the port read here cannot include it yet.
	info.removing = true
	feedback := feedbackCallbacks[key]
	delete(feedbackCallbacks, key)
	port := info.port
	mu.Unlock()
	if feedback != nil {
		feedback.inflight.Wait()
	}

	// Do not hold the C API mutex across a driver operation. Besides taking an
	// unbounded amount of time, plugout cancels endpoint workers whose output path
	// briefly takes the same mutex to observe that feedback has been quiesced.
	if port > 0 {
		if err := api.DetachLocalhostClient(context.Background(), port, slog.Default()); err != nil {
			slog.Warn("detach device failed; removing server-side device anyway", "busID", bid, "deviceID", did, "port", port, "error", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if server == nil {
		return setError(fmt.Errorf("not initialized after client detach"))
	}
	if current := devices[key]; current != info {
		return setError(fmt.Errorf("device %d-%d changed during client detach", bid, did))
	}
	delete(devices, key)
	releaseFastHandles(info)

	didStr := fmt.Sprintf("%d", did)
	if err := server.RemoveDeviceByID(bid, didStr); err != nil {
		return setError(err)
	}

	return 0
}

//export viiper_list_device_types
func viiper_list_device_types() (out *C.char) {
	defer recoverExport(nil)
	types := api.ListDeviceTypes()

	// Filter out non-controller devices and add user-friendly aliases.
	var filtered []string
	seen := make(map[string]bool, len(types))
	for _, t := range types {
		if hiddenDeviceTypes[t] {
			continue
		}
		filtered = append(filtered, t)
		seen[t] = true
	}
	for alias := range deviceTypeAliases {
		if !seen[alias] {
			filtered = append(filtered, alias)
			seen[alias] = true
		}
	}

	data, err := json.Marshal(filtered)
	if err != nil {
		return C.CString("[]")
	}
	return C.CString(string(data))
}

// ---------------------------------------------------------------------------
// Input state
// ---------------------------------------------------------------------------

//export viiper_device_set_input
func viiper_device_set_input(busID C.uint32_t, deviceID C.uint32_t, data *C.uint8_t, length C.int) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()
	defer mu.Unlock()

	if server == nil {
		return setError(fmt.Errorf("not initialized"))
	}

	key := deviceKey{busID: uint32(busID), devID: uint32(deviceID)}
	info, ok := devices[key]
	if !ok {
		return setError(fmt.Errorf("device %d-%d not found", busID, deviceID))
	}

	// Zero-copy view of the caller's buffer: decoded synchronously under mu
	// and never retained, so no C.GoBytes heap copy is needed.
	buf, ok := inputView(data, length)
	if !ok {
		return setError(fmt.Errorf("invalid input buffer length %d", int(length)))
	}
	return setError(applyInput(info, buf))
}

// applyInput decodes a wire-format input buffer and applies it to the
// device. Shared by the generic (mutex-guarded) set_input entry point and
// the lock-free handle-based fast path. buf is not retained.
func applyInput(info *deviceInfo, buf []byte) error {
	switch info.typeName {
	case "xbox360":
		xdev, ok := info.dev.(*xbox360.Xbox360)
		if !ok {
			return fmt.Errorf("device type mismatch")
		}
		var state xbox360.InputState
		if err := state.UnmarshalBinary(buf); err != nil {
			return err
		}
		xdev.UpdateInputState(state)

	case "dualshock4":
		ds4, ok := info.dev.(*dualshock4.DualShock4)
		if !ok {
			return fmt.Errorf("device type mismatch")
		}
		var state dualshock4.InputState
		if err := state.UnmarshalBinary(buf); err != nil {
			return err
		}
		ds4.UpdateInputState(&state)

	case "dualsense", "dualsenseedge":
		// One package serves both: dualsense.NewEdge builds the Edge variant.
		ds, ok := info.dev.(*dualsense.DualSense)
		if !ok {
			return fmt.Errorf("device type mismatch")
		}
		var state dualsense.InputState
		if err := state.UnmarshalBinary(buf); err != nil {
			return err
		}
		ds.UpdateInputState(&state)

	case "steamdeck":
		sd, ok := info.dev.(*steamdeck.SteamDeck)
		if !ok {
			return fmt.Errorf("device type mismatch")
		}
		var state steamdeck.InputState
		if err := state.UnmarshalBinary(buf); err != nil {
			return err
		}
		sd.UpdateInputState(&state)

	case "xboxelite2":
		xe2, ok := info.dev.(*xboxelite2.XboxElite2)
		if !ok {
			return fmt.Errorf("device type mismatch")
		}
		var state elite2state.InputState
		var err error
		switch len(buf) {
		case elite2state.InputStateSize:
			err = state.UnmarshalBinary(buf)
		case elite2state.InputStateV1Size:
			err = state.UnmarshalV1Binary(buf)
		case elite2state.LegacyInputStateSize:
			err = state.UnmarshalLegacyBinary(buf)
		default:
			err = fmt.Errorf(
				"invalid xboxelite2 input size: got %d (expected %d, %d, or %d)",
				len(buf), elite2state.InputStateSize, elite2state.InputStateV1Size, elite2state.LegacyInputStateSize,
			)
		}
		if err != nil {
			return err
		}
		xe2.UpdateInputState(&state)

	case "steamcontroller":
		sc, ok := info.dev.(*steamcontroller.SteamController)
		if !ok {
			return fmt.Errorf("device type mismatch")
		}
		// Gordon uses its native 64-byte input format.
		var state steamcontroller.InputState
		if err := state.UnmarshalBinary(buf); err != nil {
			return err
		}
		sc.UpdateInputState(&state)

	case "switchpro":
		sp, ok := info.dev.(*switchpro.SwitchPro)
		if !ok {
			return fmt.Errorf("device type mismatch")
		}
		var state switchpro.InputState
		if err := state.UnmarshalBinary(buf); err != nil {
			return err
		}
		sp.UpdateInputState(&state)

	case "xboxgip":
		xdev, ok := info.dev.(*xboxgip.XboxGIP)
		if !ok {
			return fmt.Errorf("device type mismatch")
		}
		var state xboxgip.InputState
		if err := state.UnmarshalBinary(buf); err != nil {
			return err
		}
		xdev.UpdateInputState(&state)

	case "ns2pro":
		// Switch 2 Pro Controller: 24-byte wire (Buttons:u32, LX/LY/RX/RY:u16
		// 12-bit native, Accel/Gyro:i16). Battery and charging state are
		// device metadata now, not input. UpdateInputState takes the
		// InputState by value (not pointer) to match the port's API.
		ns2, ok := info.dev.(*ns2pro.NS2Pro)
		if !ok {
			return fmt.Errorf("device type mismatch")
		}
		var state ns2pro.InputState
		if err := state.UnmarshalBinary(buf); err != nil {
			return err
		}
		ns2.UpdateInputState(state)

	default:
		return fmt.Errorf("input not supported for device type: %s", info.typeName)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Feedback callbacks
// ---------------------------------------------------------------------------

//export viiper_device_set_feedback_callback
func viiper_device_set_feedback_callback(busID C.uint32_t, deviceID C.uint32_t, cb C.viiper_feedback_fn, userData unsafe.Pointer) (rc C.int) {
	defer recoverExport(&rc)
	mu.Lock()
	defer mu.Unlock()

	if server == nil {
		return setError(fmt.Errorf("not initialized"))
	}

	key := deviceKey{busID: uint32(busID), devID: uint32(deviceID)}
	info, ok := devices[key]
	if !ok {
		return setError(fmt.Errorf("device %d-%d not found", busID, deviceID))
	}

	reg := &feedbackReg{fn: cb, userData: userData}
	feedbackCallbacks[key] = reg

	bid := uint32(busID)
	did := uint32(deviceID)

	switch info.typeName {
	case "xbox360":
		xdev, ok := info.dev.(*xbox360.Xbox360)
		if !ok {
			return setError(fmt.Errorf("device type mismatch"))
		}
		xdev.SetRumbleCallback(func(rumble xbox360.XRumbleState) {
			data, err := rumble.MarshalBinary()
			if err != nil {
				return
			}
			invokeFeedbackCallback(bid, did, data)
		})

	case "dualshock4":
		ds4, ok := info.dev.(*dualshock4.DualShock4)
		if !ok {
			return setError(fmt.Errorf("device type mismatch"))
		}
		ds4.SetOutputCallback(func(output dualshock4.OutputState) {
			data, err := output.MarshalBinary()
			if err != nil {
				return
			}
			invokeFeedbackCallback(bid, did, data)
		})

	case "dualsense", "dualsenseedge":
		ds, ok := info.dev.(*dualsense.DualSense)
		if !ok {
			return setError(fmt.Errorf("device type mismatch"))
		}
		ds.SetOutputCallback(func(output dualsense.OutputState) {
			data, err := output.MarshalBinary()
			if err != nil {
				return
			}
			invokeFeedbackCallback(bid, did, data)
		})

	case "steamdeck":
		sd, ok := info.dev.(*steamdeck.SteamDeck)
		if !ok {
			return setError(fmt.Errorf("device type mismatch"))
		}
		sd.SetOutputCallback(func(output steamdeck.OutputState) {
			data, err := output.MarshalBinary()
			if err != nil {
				return
			}
			invokeFeedbackCallback(bid, did, data)
		})

	case "xboxelite2":
		xe2, ok := info.dev.(*xboxelite2.XboxElite2)
		if !ok {
			return setError(fmt.Errorf("device type mismatch"))
		}
		xe2.SetOutputCallback(func(output elite2state.OutputState) {
			data, err := output.MarshalBinary()
			if err != nil {
				return
			}
			invokeFeedbackCallback(bid, did, data)
		})

	case "steamcontroller":
		sc, ok := info.dev.(*steamcontroller.SteamController)
		if !ok {
			return setError(fmt.Errorf("device type mismatch"))
		}
		sc.SetOutputCallback(func(output steamcontroller.OutputState) {
			data, err := output.MarshalBinary()
			if err != nil {
				return
			}
			invokeFeedbackCallback(bid, did, data)
		})

	case "switchpro":
		sp, ok := info.dev.(*switchpro.SwitchPro)
		if !ok {
			return setError(fmt.Errorf("device type mismatch"))
		}
		sp.SetOutputCallback(func(output switchpro.OutputState) {
			data, err := output.MarshalBinary()
			if err != nil {
				return
			}
			invokeFeedbackCallback(bid, did, data)
		})

	case "xboxgip":
		xdev, ok := info.dev.(*xboxgip.XboxGIP)
		if !ok {
			return setError(fmt.Errorf("device type mismatch"))
		}
		xdev.SetOutputCallback(func(output xboxgip.OutputState) {
			data, err := output.MarshalBinary()
			if err != nil {
				return
			}
			invokeFeedbackCallback(bid, did, data)
		})

	case "ns2pro":
		ns2, ok := info.dev.(*ns2pro.NS2Pro)
		if !ok {
			return setError(fmt.Errorf("device type mismatch"))
		}
		ns2.SetOutputCallback(func(output ns2pro.OutputState) {
			data, err := output.MarshalBinary()
			if err != nil {
				return
			}
			invokeFeedbackCallback(bid, did, data)
		})

	default:
		return setError(fmt.Errorf("feedback not supported for device type: %s", info.typeName))
	}

	return 0
}

// invokeFeedbackCallback calls the registered C callback for a device.
// Must NOT hold mu when calling this (the callback may re-enter).
func invokeFeedbackCallback(busID, deviceID uint32, data []byte) {
	mu.Lock()
	key := deviceKey{busID: busID, devID: deviceID}
	reg, ok := feedbackCallbacks[key]
	if !ok || reg.fn == nil {
		mu.Unlock()
		return
	}
	reg.inflight.Add(1)
	mu.Unlock()
	defer reg.inflight.Done()

	cData := C.CBytes(data)
	defer C.free(cData)

	C.bridge_feedback_fn(reg.fn, C.uint32_t(busID), C.uint32_t(deviceID),
		(*C.uint8_t)(cData), C.int(len(data)), reg.userData)
}

func main() {}
