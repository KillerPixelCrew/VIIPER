//go:build windows

package usb_test

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Alia5/VIIPER/device/steamdeck"
	usbsrv "github.com/Alia5/VIIPER/internal/server/usb"
	"github.com/Alia5/VIIPER/usbip"
	"github.com/Alia5/VIIPER/virtualbus"
)

// TestURBCycleCost measures what one interrupt-IN completion costs the server process: CPU time,
// thread context switches, heap allocations and garbage collections per completed URB. It runs
// the real server with the Steam Deck device and drives it from a second process that behaves
// like the usbip-win2 host: one URB in flight per interrupt-IN endpoint, resubmitted the moment
// the previous one completes, with the keyboard and mouse URBs left pending the way the Windows
// HID stack leaves them. Keeping the host in its own process keeps its syscalls out of the
// measurement.
//
// It is a measurement, not a regression test, so it only runs when VIIPER_URB_BENCH=1:
//
//	VIIPER_URB_BENCH=1 go test ./internal/server/usb -run TestURBCycleCost -v
//
// Knobs, all environment variables: VIIPER_URB_BENCH_SECONDS (measured window, default 5),
// VIIPER_URB_BENCH_INPUT_HZ (input updates per second, default 0 for an untouched pad),
// VIIPER_URB_BENCH_GOMAXPROCS (default: what libviiper picks), VIIPER_IDLE_MODE (auto, nak or
// keepalive), VIIPER_IDLE_KEEPALIVE_INTERVAL (how often an idle endpoint repeats its last report)
// and VIIPER_URB_BENCH_PROFILE (write a pprof CPU profile of the measured window).
//
// Read Mcycles/s and ctxsw/s to compare configurations: the per-completion figures divide by a
// completion count the idle repeat rate itself changes.
func TestURBCycleCost(t *testing.T) {
	if addr := os.Getenv("VIIPER_URB_BENCH_HOST"); addr != "" {
		runBenchHost(t, addr)
		return
	}
	if os.Getenv("VIIPER_URB_BENCH") != "1" {
		t.Skip("set VIIPER_URB_BENCH=1 to run the URB cycle measurement")
	}

	seconds := envInt("VIIPER_URB_BENCH_SECONDS", 5)
	inputHz := envInt("VIIPER_URB_BENCH_INPUT_HZ", 0)
	maxProcs := envInt("VIIPER_URB_BENCH_GOMAXPROCS", 0)
	if maxProcs == 0 && runtime.NumCPU() > 4 {
		maxProcs = 4
	}
	if maxProcs > 0 {
		runtime.GOMAXPROCS(maxProcs)
	}
	idleMode := os.Getenv("VIIPER_IDLE_MODE")
	if idleMode == "" {
		idleMode = "auto"
	}
	idleKeepalive := 64 * time.Millisecond
	if v := os.Getenv("VIIPER_IDLE_KEEPALIVE_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		idleKeepalive = d
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := usbsrv.New(usbsrv.ServerConfig{
		Addr:                     "127.0.0.1:0",
		ConnectionTimeout:        30 * time.Second,
		BusCleanupTimeout:        5 * time.Second,
		HardwarePacedCompletions: true,
		IdleMode:                 idleMode,
		IdleKeepaliveInterval:    idleKeepalive,
	}, logger, nil)
	go func() { _ = server.ListenAndServe() }()
	<-server.Ready()
	if err := server.ReadyErr(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	bus, err := virtualbus.NewWithBusID(1)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
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

	stopInput := make(chan struct{})
	if inputHz > 0 {
		go func() {
			ticker := time.NewTicker(time.Second / time.Duration(inputHz))
			defer ticker.Stop()
			var state steamdeck.InputState
			for {
				select {
				case <-stopInput:
					return
				case <-ticker.C:
					state.LStickX++
					deck.UpdateInputState(&state)
				}
			}
		}()
	}
	defer close(stopInput)

	host := exec.Command(os.Args[0], "-test.run", "^TestURBCycleCost$", "-test.v")
	host.Env = append(os.Environ(),
		"VIIPER_URB_BENCH_HOST="+server.Addr(),
		"VIIPER_URB_BENCH_SECONDS="+strconv.Itoa(seconds+2))
	host.Stderr = os.Stderr
	hostOut, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	hostLines := bufio.NewScanner(hostOut)
	// The host reports when its URB loop is running so the measured window excludes the
	// handshake and the process start.
	for hostLines.Scan() {
		if strings.HasPrefix(hostLines.Text(), "host running") {
			break
		}
	}

	time.Sleep(time.Second)
	profiling := false
	if path := os.Getenv("VIIPER_URB_BENCH_PROFILE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
		profiling = true
	}
	before := sampleProcess(t)
	time.Sleep(time.Duration(seconds) * time.Second)
	after := sampleProcess(t)
	if profiling {
		// Stopped here rather than deferred: Go's Windows profiler samples a thread blocked in
		// a pipe read as if it were running, and the host's stdout is read next.
		pprof.StopCPUProfile()
	}

	completions := -1
	for hostLines.Scan() {
		line := hostLines.Text()
		if n, ok := strings.CutPrefix(line, "host completions "); ok {
			completions, _ = strconv.Atoi(strings.TrimSpace(n))
		}
	}
	if err := host.Wait(); err != nil {
		t.Fatalf("host process: %v", err)
	}
	if completions < 0 {
		t.Fatal("host did not report its completion count")
	}
	// The host counts over its own longer window; scale to the measured one.
	perSecond := float64(completions) / float64(seconds+2)
	measured := perSecond * float64(seconds)

	// Process cycle time is accumulated by the kernel at every context switch, so it is exact
	// where GetProcessTimes samples on the clock tick and misses most of a 20 µs burst.
	cycles := after.cycles - before.cycles
	cpu := after.cpu - before.cpu
	kernel := after.kernel - before.kernel
	result := fmt.Sprintf(
		"RESULT input=%dHz gomaxprocs=%d idle=%s/%s completions/s=%.1f Mcycles/s=%.1f kcycles/completion=%.0f cpu%%=%.2f kernel_share=%.2f ctxsw/s=%.0f ctxsw/completion=%.2f mallocs/completion=%.1f bytes/completion=%.0f gc/min=%.1f",
		inputHz, runtime.GOMAXPROCS(0), idleMode, idleKeepalive,
		perSecond,
		float64(cycles)/1e6/float64(seconds),
		float64(cycles)/1e3/measured,
		100*cpu.Seconds()/float64(seconds),
		kernel.Seconds()/cpu.Seconds(),
		float64(after.contextSwitches-before.contextSwitches)/float64(seconds),
		float64(after.contextSwitches-before.contextSwitches)/measured,
		float64(after.mallocs-before.mallocs)/measured,
		float64(after.bytes-before.bytes)/measured,
		60*float64(after.gcs-before.gcs)/float64(seconds))
	t.Log(result)
	fmt.Println(result)
}

type processSample struct {
	cpu, kernel     time.Duration
	cycles          uint64
	contextSwitches uint64
	mallocs, bytes  uint64
	gcs             uint32
}

var procQueryProcessCycleTime = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryProcessCycleTime")

func sampleProcess(t *testing.T) processSample {
	t.Helper()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	var cycles uint64
	if r, _, err := procQueryProcessCycleTime.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&cycles))); r == 0 {
		t.Fatal(err)
	}
	k := time.Duration(kernel.Nanoseconds())
	u := time.Duration(user.Nanoseconds())
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return processSample{
		cpu:             k + u,
		kernel:          k,
		cycles:          cycles,
		contextSwitches: processContextSwitches(t),
		mallocs:         stats.Mallocs,
		bytes:           stats.TotalAlloc,
		gcs:             stats.NumGC,
	}
}

// processContextSwitches sums the context switch counters of every thread of this process, the
// figure Windows Performance Analyzer reports as wakeups.
func processContextSwitches(t *testing.T) uint64 {
	t.Helper()
	const systemProcessInformation = 5
	// SYSTEM_THREAD_INFORMATION on x64: KernelTime, UserTime, CreateTime (int64), WaitTime
	// (uint32, padded), StartAddress, ClientId (two pointers), Priority, BasePriority,
	// ContextSwitches, ThreadState, WaitReason, padding.
	const threadInfoSize = 80
	const contextSwitchesOffset = 64
	buf := make([]byte, 1<<20)
	for {
		var needed uint32
		err := windows.NtQuerySystemInformation(systemProcessInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &needed)
		if err == nil {
			break
		}
		if err == windows.STATUS_INFO_LENGTH_MISMATCH || err == windows.STATUS_BUFFER_TOO_SMALL {
			buf = make([]byte, int(needed)+64*1024)
			continue
		}
		t.Fatal(err)
	}
	pid := uintptr(os.Getpid())
	offset := 0
	for {
		info := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buf[offset]))
		if info.UniqueProcessID == pid {
			var total uint64
			threads := offset + int(unsafe.Sizeof(*info))
			for i := 0; i < int(info.NumberOfThreads); i++ {
				at := threads + i*threadInfoSize + contextSwitchesOffset
				total += uint64(binary.LittleEndian.Uint32(buf[at : at+4]))
			}
			return total
		}
		if info.NextEntryOffset == 0 {
			t.Fatal("process not found in NtQuerySystemInformation output")
		}
		offset += int(info.NextEntryOffset)
	}
}

// runBenchHost is the usbip-win2 stand-in: import the device, leave the keyboard and mouse URBs
// pending, and keep one controller URB in flight for the whole window.
func runBenchHost(t *testing.T, addr string) {
	seconds := envInt("VIIPER_URB_BENCH_SECONDS", 7)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	if err := (&usbip.MgmtHeader{Version: usbip.Version, Command: usbip.OpReqImport}).Write(conn); err != nil {
		t.Fatal(err)
	}
	var busID [32]byte
	copy(busID[:], "1-1")
	if _, err := conn.Write(busID[:]); err != nil {
		t.Fatal(err)
	}
	// OP_REP_IMPORT: 8-byte header plus the 312-byte exported device.
	var reply [8 + 312]byte
	if err := usbip.ReadExactly(conn, reply[:]); err != nil {
		t.Fatal(err)
	}
	if status := binary.BigEndian.Uint32(reply[4:8]); status != 0 {
		t.Fatalf("import status %d", status)
	}

	seq := uint32(1)
	submit := func(ep, length uint32) {
		cmd := usbip.CmdSubmit{
			Basic:             usbip.HeaderBasic{Command: usbip.CmdSubmitCode, Seqnum: seq, Dir: usbip.DirIn, Ep: ep},
			TransferBufferLen: length,
			Interval:          6,
		}
		seq++
		if err := cmd.Write(conn); err != nil {
			t.Fatal(err)
		}
	}
	submit(1, 8)
	submit(2, 4)
	submit(3, 64)

	fmt.Println("host running")
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	// In NAK-idle mode an untouched controller endpoint never completes, so the read must end
	// with the window rather than with a completion.
	_ = conn.SetReadDeadline(deadline)
	completions := 0
	var header [48]byte
	payload := make([]byte, 64)
	for time.Now().Before(deadline) {
		if err := usbip.ReadExactly(conn, header[:]); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			t.Fatal(err)
		}
		actual := binary.BigEndian.Uint32(header[24:28])
		if actual > uint32(len(payload)) {
			t.Fatalf("payload of %d bytes", actual)
		}
		if err := usbip.ReadExactly(conn, payload[:actual]); err != nil {
			t.Fatal(err)
		}
		completions++
		submit(3, 64)
	}
	fmt.Println("host completions", completions)
}

func envInt(name string, fallback int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
