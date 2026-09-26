# NOTICE

This repository is a **fork of VIIPER**, originally created by **Alia5**:

- Upstream project: https://github.com/Alia5/VIIPER
- Upstream license: **GNU General Public License v3.0** (see `LICENSE.txt`)
- Copyright © the VIIPER authors (Alia5 and contributors)

VIIPER is free software licensed under the GPL-3.0. This fork remains licensed
under the **GPL-3.0** in its entirety; the full license text is preserved in
`LICENSE.txt`. The original copyright and license notices are retained.

> NOTE ON CLIENT LIBRARIES: upstream VIIPER additionally offers its generated
> *client libraries* (C#, Rust, npm, C++ headers) under the MIT license. Those
> MIT terms apply only to the thin client wrappers that communicate with a
> VIIPER server over its socket API. They do **not** apply to `libviiper.dll`,
> which is built from `./clib/` and statically links the GPL-3.0 core
> (`device/*`, `internal/server`, `internal/registry`, ...). `libviiper.dll` is
> therefore a GPL-3.0 work.

## Modifications in this fork

The `wsgm` branch tracks Alia5/VIIPER `main` by merging it. Relative to
upstream, it adds or changes (non-exhaustive):

- `clib`: a C shared library (`libviiper.dll`) with a single embedded server,
  add and attach as separate calls, per-type input fast paths, raw feedback
  callbacks that are drained before a device is removed, usbip client plug-out
  on remove, panic recovery at the cgo boundary, a capped `GOMAXPROCS` default,
  and a device-type alias system (handheld VID/PID overrides and deprecation
  warnings).
- `internal/server/usb`: persistent per-endpoint interrupt-IN workers,
  hardware-paced completions, per-device NAK-idle endpoints, a paced repeat of an
  unchanged report once an endpoint is idle, and an allocation-free completion
  path for devices that implement `usb.InterruptInSource`, in place of upstream's
  per-URB completion goroutines.
- Windows attach: a cancellable overlapped `plugin_hardware` IOCTL that
  negotiates the usbip-win2 0.9.7.7, 0.9.7.8 and 0.9.8.0 layouts, and requests
  the low-latency (WSK event) receive mode on 0.9.8.0.
- New device backends: `device/steamdeck` (the Steam Deck handheld controller),
  `device/steamcontroller` (the wired Steam Controller V1), `device/switchpro`,
  `device/xboxelite2`, and `device/xboxgip` with `cmd/gip_probe` (GIP protocol
  experimentation, blocked by a missing Microsoft-side auth challenge).
- `device/xbox360`, `keyboard`, `mouse`, `dualshock4`: input is signalled
  through a shared input gate and stored by value, so updates do not allocate.
  The DualShock 4 calibration report declares VIIPER's accel scale as 1g.
- Paced interrupt-IN endpoints complete on their `bInterval` grid with the
  state current at each poll, and the Steam Deck numbers every report it sends
  and reports the mean gyro rate over each poll interval rather than the latest
  sample, so a host that integrates the gyro per report sees the rotation the
  client's samples described whatever the sensor's cadence
  (`docs/wsgm-idle-endpoints.md`).

`device/dualsense` and `device/ns2pro` are upstream's implementations.

## How to obtain the corresponding source

The complete corresponding source for `libviiper.dll` is this repository. Anyone
who receives a binary built from it (including `libviiper.dll` as distributed by
downstream projects) is entitled under GPL-3.0 §6 to the corresponding source at:

  https://github.com/KillerPixelCrew/VIIPER/tree/wsgm

Built artifacts are produced via `build_dll.bat` from the `./clib/` package.
