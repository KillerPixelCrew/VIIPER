# Idle interrupt-IN endpoints

What an emulated device costs while nobody is touching it, and how the downstream branch keeps that
cost down. The short version: fresh input is always delivered immediately, and an endpoint that has
nothing new to say is as quiet as its consumer allows.

## Placeholder endpoints

In automatic idle mode the Steam Deck keyboard and mouse placeholder endpoints wait until their host
request is cancelled. They emit no reports, and no timer runs for them.

Previously the placeholders stayed pending on USB, but the server applied a new keepalive deadline
on every attempt. Each timeout returned no data and started another attempt, which repeatedly woke
Go timers for endpoints that could never produce a report.

Composite devices can implement `NaksWhenIdleForEndpoint(uint32) bool`. Automatic mode checks this
before the older device-wide `NaksWhenIdle() bool` declaration. Explicit `nak` and `keepalive`
configuration still take precedence. Devices without either declaration retain keepalive behavior.

## The keepalive repeat

A device whose real hardware streams continuously, the Steam Deck and the DualShock 4 among them,
keeps sending its last report whenever the endpoint's `bInterval` passes with no fresh input, so a
consumer watching the report rate sees the stream it expects.

For a controller nobody is touching, that repeat is the entire cost of the emulation. At the Deck's
6 ms `bInterval` it is about 166 loopback socket writes a second, each carrying bytes the host
already has. Measured with `internal/server/usb/urbcycle_bench_test.go` on an MSI Claw 8 AI+, that
came to 107 Mcycles/s and 678 thread context switches a second, roughly 3.5 % of one core, spent on
a device in a drawer.

`IdleKeepaliveInterval` (`VIIPER_IDLE_KEEPALIVE_INTERVAL`, default 64 ms) paces that repeat once an
endpoint has gone quiet. The first repeat still arrives one `bInterval` after the last real input,
so a device that is streaming looks unchanged; only an endpoint that has already repeated itself
slows down, and it returns to its `bInterval` the moment real input arrives. Fresh input never waits
for either timer, so there is no input latency to trade away. Set the value to 0 to repeat every
`bInterval` as before.

| Idle repeat | Completions/s | Mcycles/s | Context switches/s |
| ----------- | ------------- | --------- | ------------------ |
| 6 ms        | 146           | 107       | 678                |
| 64 ms       | 15.5          | 14        | 78                 |
| 250 ms      | 4.1           | 5.7       | 21                 |

## Data-driven completions

A device that implements `usb.InterruptInSource` is served without a call per poll. The server owns
the endpoint's poll timer and its completion frame: it waits on the device's own input channel and a
timer it reuses, and an endpoint whose state has not changed is completed straight from the frame it
already holds, with the sequence number written over the header in place.

That removes, per completion, a context with a deadline, a context with a cancel, a report
allocation inside the device and the copy into the server's replay cache. On the same machine an
idle Steam Deck endpoint went from 23 heap allocations per completion to 1, and from 1,324 to 735
thousand cycles per completion. Devices that do not implement the interface keep the previous path,
one `HandleTransfer` call per URB with a context per URB, and still get the idle repeat pacing.

The Steam Deck implements it. Its keyboard and mouse endpoints return a nil input channel, which is
how a placeholder endpoint says it has nothing of its own to send.

## GOMAXPROCS

`libviiper` runs the server with `GOMAXPROCS=1` unless `VIIPER_GOMAXPROCS` says otherwise. The work
per URB is microseconds, and with more than one P every URB is handed from the reading goroutine to
its endpoint worker across OS threads: three more context switches per completion and about a third
more CPU, measured. A caller's input update still runs on its own thread and borrows the idle P for
the microseconds it takes.

## Validation status

The library was compiled and measured with the harness above. Live controller, gyro and rumble
validation against Steam is the maintainer's manual check; the idle repeat interval is the one
change a consumer could notice, and `VIIPER_IDLE_KEEPALIVE_INTERVAL=0` restores the old cadence
without a rebuild.
