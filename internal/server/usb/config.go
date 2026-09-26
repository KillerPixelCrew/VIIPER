package usb

import "time"

// ServerConfig represents the server subcommand configuration.
type ServerConfig struct {
	Addr                    string        `help:"USB-IP server listen address" default:":3241" env:"VIIPER_USB_ADDR"`
	ConnectionTimeout       time.Duration `kong:"-"`
	BusCleanupTimeout       time.Duration `help:"-"`
	WriteBatchFlushInterval time.Duration `help:"Interval to flush write batches to clients; 0 to disable" default:"0" env:"VIIPER_USB_WRITE_BATCH_FLUSH_INTERVAL"`
	// HardwarePacedCompletions completes interrupt-IN URBs at each endpoint's
	// bInterval (like real USB hardware polls) instead of once per input
	// update. Input state is conflated latest-wins between completions. At
	// input rates above the poll rate this proportionally cuts TCP round
	// trips and kernel URB work; below the poll rate behavior is unchanged.
	HardwarePacedCompletions bool `help:"Pace interrupt-IN completions to the endpoint bInterval instead of per input update" default:"true" env:"VIIPER_HW_PACED"`
	// IdleMode controls interrupt-IN endpoints with no fresh input:
	//   "auto" (default): per-endpoint when declared, otherwise per-device.
	//     Steam Deck keyboard/mouse placeholders wait without timeout attempts;
	//     its controller endpoint retains continuous keepalive reports.
	//     Devices whose real hardware is
	//     event-driven (Xbox family) NAK when idle; devices whose real
	//     hardware streams continuously (DS4/DualSense/Deck/Switch) replay
	//     the last report at each bInterval so consumers keep seeing the
	//     stream they expect.
	//   "nak": force NAK-idle for all devices (zero idle traffic).
	//   "keepalive": force bInterval keepalive replays for all devices.
	IdleMode string `help:"Idle interrupt-IN behavior: auto, nak, or keepalive" default:"auto" env:"VIIPER_IDLE_MODE"`
	// IdleKeepaliveInterval paces the repeat of a report that has not changed. A keepalive
	// endpoint sends its last report again whenever its poll interval passes with no fresh
	// input, and for an untouched controller that is the whole cost of the emulation: one
	// loopback write and one timer every bInterval, carrying bytes the host already has. After
	// the first such repeat the endpoint waits this long instead, and returns to its bInterval
	// the moment real input arrives. Values below the endpoint's bInterval, and 0, mean every
	// bInterval.
	//
	// It defaults to 0 because 64 ms made the gyro stutter on an MSI Claw and a ROG Ally, which
	// a device-level A/B pinned to this setting alone (2026-09-26). Fresh input never waits for
	// the timer, so the cause is not input latency and is not yet understood; opt in to a slower
	// repeat only where it has been measured on the consumer that will see it.
	IdleKeepaliveInterval time.Duration `help:"How often an unchanged interrupt-IN report is repeated once an endpoint is idle" default:"0" env:"VIIPER_IDLE_KEEPALIVE_INTERVAL"`
}
