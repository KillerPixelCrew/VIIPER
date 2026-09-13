# Idle Steam Deck endpoints

In automatic idle mode the Steam Deck keyboard and mouse placeholder endpoints wait until their
host request is cancelled. They emit no reports. The controller endpoint keeps its existing
hardware-paced, continuous report stream.

Previously the placeholders stayed pending on USB, but the server applied a new keepalive deadline
on every attempt. Each timeout returned no data and started another attempt. This repeatedly woke
Go timers for endpoints that could never produce a report.

Composite devices can implement `NaksWhenIdleForEndpoint(uint32) bool`. Automatic mode checks this
before the older device-wide `NaksWhenIdle() bool` declaration. Explicit `nak` and `keepalive`
configuration still take precedence. Devices without either declaration retain keepalive behavior.

The DLL and regression-test binaries were compiled for this change. Test execution and live
controller/gyro/rumble validation are deferred until the maintainer's manual check. No measured CPU
reduction is claimed before that deployment.
