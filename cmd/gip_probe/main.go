// gip_probe — isolated experiment driver for the xboxgip target.
//
// Assumes a running viiper.exe server (start with: viiper.exe server).
// Creates a single xboxgip device with optional VID/PID overrides, waits
// for the GIP protocol to play out, then prints the protocol log slice
// and the Windows PnP state for the resulting device.
//
// Iterate by:
//  1. Edit device/xboxgip/{device.go,protocol.go,...} in this repo.
//  2. go build -o gip_probe.exe ./cmd/gip_probe
//  3. go build -o viiper.exe ./cmd/viiper        (server rebuild)
//  4. Restart viiper.exe and rerun gip_probe.exe.
//
// Each iteration ~30s of Go build + ~10s of test. No MSIX install,
// no GoTweaks helper restart.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Alia5/VIIPER/device"
	"github.com/Alia5/VIIPER/viiperclient"
)

func main() {
	addr := flag.String("addr", "localhost:3242", "viiper API address")
	vid := flag.Int("vid", 0x045E, "VID override")
	pid := flag.Int("pid", 0x02D1, "PID override (0 = use libviiper default)")
	waitSec := flag.Int("wait", 10, "seconds to wait for protocol to play out")
	keep := flag.Bool("keep", false, "leave device attached after run (skip remove)")
	flag.Parse()

	ctx := context.Background()
	api := viiperclient.New(*addr)

	// Find or create a bus.
	busesResp, err := api.BusListCtx(ctx)
	if err != nil {
		fatalf("BusList error (is viiper.exe server running on %s?): %v", *addr, err)
	}
	var busID uint32
	if len(busesResp.Buses) == 0 {
		r, err := api.BusCreateCtx(ctx, 0)
		if err != nil {
			fatalf("BusCreate failed: %v", err)
		}
		busID = r.BusID
		fmt.Printf("[+] created bus %d\n", busID)
	} else {
		busID = busesResp.Buses[0]
		fmt.Printf("[+] using existing bus %d\n", busID)
	}

	// Build device create options.
	opts := &device.CreateOptions{}
	if *vid != 0 {
		v := uint16(*vid)
		opts.IDVendor = &v
	}
	if *pid != 0 {
		p := uint16(*pid)
		opts.IDProduct = &p
	}

	dev, err := api.DeviceAddCtx(ctx, busID, "xboxgip", opts)
	if err != nil {
		fatalf("DeviceAdd xboxgip failed: %v", err)
	}
	fmt.Printf("[+] added xboxgip device devId=%s vid=%s pid=%s\n", dev.DevID, dev.Vid, dev.Pid)

	// Wait for protocol to play out.
	fmt.Printf("[+] waiting %ds for GIP protocol...\n", *waitSec)
	time.Sleep(time.Duration(*waitSec) * time.Second)

	// Read xboxgip_debug.log alongside this exe.
	summary := summarizeGipLog(findGipLog())
	fmt.Println("--- log summary ---")
	fmt.Println(summary.text)
	fmt.Println("---")
	fmt.Printf("[+] outcome=%s\n", summary.outcome)

	// Detect IGamepad PDO via pnputil.
	gp := detectGamepadPDO()
	fmt.Printf("[+] gamepad_present=%s\n", gp)

	if !*keep {
		_, _ = api.DeviceRemoveCtx(ctx, busID, dev.DevID)
		fmt.Printf("[+] removed device %s\n", dev.DevID)
	}

	fmt.Println("=== RESULT outcome=" + summary.outcome + " gamepad_present=" + gp + " ===")
}

type logSummary struct {
	text    string
	outcome string
}

func summarizeGipLog(path string) logSummary {
	if path == "" {
		return logSummary{text: "(log file not found)", outcome: "LOG_MISSING"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return logSummary{text: "log read error: " + err.Error(), outcome: "LOG_READ_ERR"}
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 250 {
		lines = lines[len(lines)-250:]
	}

	keepers := []string{}
	keywords := []string{
		"GIP device created", "VID=", "Hello", "EP OUT", "SetState",
		"Quiesce", "→ Idle", "→ Active", "first input report",
		"metadata request received", "drained", "EP0:", "Off",
		"queued response", "queued metadata fragment", "queued metadata complete",
	}
	for _, ln := range lines {
		for _, k := range keywords {
			if strings.Contains(ln, k) {
				keepers = append(keepers, ln)
				break
			}
		}
	}
	if len(keepers) > 80 {
		keepers = keepers[len(keepers)-80:]
	}

	full := strings.Join(keepers, "\n")
	outcome := "NO_PROTOCOL_ACTIVITY"
	switch {
	case strings.Contains(full, "SetState code=0x00"):
		outcome = "START_RECEIVED"
	case strings.Contains(full, "first input report"):
		outcome = "REACHED_ACTIVE_NO_START"
	case strings.Contains(full, "SetState code=0x05"):
		outcome = "REACHED_QUIESCE_NO_ACTIVE"
	case strings.Contains(full, "SetState code=0x01"):
		outcome = "STOPPED_AFTER_METADATA"
	case strings.Contains(full, "metadata request received"):
		outcome = "METADATA_ONLY_NO_SETSTATE"
	case strings.Contains(full, "Hello"):
		outcome = "HELLO_ONLY"
	}
	return logSummary{text: full, outcome: outcome}
}

func findGipLog() string {
	exe, _ := os.Executable()
	candidates := []string{
		strings.Replace(exe, "gip_probe.exe", "xboxgip_debug.log", 1),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if _, err := os.Stat("xboxgip_debug.log"); err == nil {
		return "xboxgip_debug.log"
	}
	return ""
}

func detectGamepadPDO() string {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"pnputil.exe /enum-devices /class XboxComposite")
	out, _ := cmd.CombinedOutput()
	s := string(out)
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "Instance ID:") && (strings.Contains(ln, "VID_045E") && (strings.Contains(ln, "IG_") || strings.Contains(ln, "MI_"))) {
			return "YES_IG_" + strings.TrimSpace(strings.TrimPrefix(ln, "Instance ID:"))
		}
	}
	cmd2 := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"pnputil.exe /enum-devices /class HIDClass")
	out2, _ := cmd2.CombinedOutput()
	for _, ln := range strings.Split(string(out2), "\n") {
		if strings.Contains(ln, "VID_045E") {
			return "YES_HID"
		}
	}
	return "NO"
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}
