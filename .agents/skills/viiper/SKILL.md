---
name: viiper
description:
  Implement or review VIIPER virtual USB devices, reports, transports and client bindings. Use for
  the controller backend; consumer routing and physical handheld policy remain outside.
---

# VIIPER contributions

Resolve aliases to this owning repository. Start with README.md and the relevant device, transport
and client documentation under docs. Follow the existing device, USBIP, protocol and generated
binding owners; do not introduce WSGM profiles or physical handheld detection here.

Use repository Go/native formatting and lint configuration. Preserve wire/report layouts and public
ABI across clients; edit the owning source/generator instead of generated bindings. Trace report
submission, rest suppression/repeat cadence, cancellation, disconnect and device destruction. A
submitted report is not evidence the host consumed it. Preserve held-edge release and lifetime
ordering; high-rate report paths allocate and log nothing per sample. Do not drop release reports or
replay stale held state when coalescing or reconnecting.

Keep pure report fixtures separate from USB attachment and physical/controller acceptance. In a WSGM
checkout follow its maintainer's validation budget; code changes require eng/build-viiper.ps1
-Validate, while skill-only edits require no native rebuild. Do not install, attach devices or
deploy from a documentation/review request. Publish changes on the consumer's authorized branch
before its gitlink is updated.
