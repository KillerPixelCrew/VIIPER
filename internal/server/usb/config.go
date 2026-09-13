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
}
