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

Relative to upstream VIIPER, this fork adds/changes (non-exhaustive):

- New device backends ported/adapted: `device/dualsense`, `device/steamdeck`,
  `device/ns2pro`.
- Reworked `device/steamcontroller` into the wired Steam Controller V1 (Gordon),
  with the Steam Deck handheld split into its own `device/steamdeck`.
- Fixed L2/R2 analog-trigger HID usages in `device/dualsense` and
  `device/dualshock4` (use Rx/Ry `0x33`/`0x34` instead of duplicating the
  right-stick `Z`/`Rz` usages) so the devices enumerate correctly as
  `Windows.Gaming.Input` RawGameControllers.
- `usb`: slim `Configuration` descriptor shape, MS OS 1.0 probe string, and a
  `Descriptor.NumInterfaces()` helper used by the ported backends.
- `clib`: device-type alias system (handheld VID/PID overrides + deprecation
  warnings) and per-device input-state / output-callback handling.
- `device/xboxgip` + `cmd/gip_probe`: GIP probe/protocol experimentation
  (currently blocked upstream of this fork by a missing Microsoft-side auth
  challenge).

## How to obtain the corresponding source

The complete corresponding source for `libviiper.dll` is this repository. Anyone
who receives a binary built from it (including `libviiper.dll` as distributed by
downstream projects) is entitled under GPL-3.0 §6 to the corresponding source at:

  https://github.com/KillerPixelCrew/VIIPER/tree/wsgm

Built artifacts are produced via `build_dll.bat` from the `./clib/` package.
