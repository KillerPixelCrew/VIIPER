# Idle interrupt-IN endpoints

What an emulated device costs while nobody is touching it, and how the downstream branch keeps that
cost down. The short version: a paced endpoint reports on its `bInterval` grid with the state it has
at each poll, the way a host reads real hardware, and an endpoint that has nothing new to say is as
quiet as its consumer allows.

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

`IdleKeepaliveInterval` (`VIIPER_IDLE_KEEPALIVE_INTERVAL`) paces that repeat once an endpoint has
gone quiet. The first repeat still arrives one `bInterval` after the last real input; only an
endpoint that has already repeated itself slows down, and it returns to its `bInterval` the moment
real input arrives. 0, the default, repeats every `bInterval`.

| Idle repeat | Completions/s | Mcycles/s | Context switches/s |
| ----------- | ------------- | --------- | ------------------ |
| 6 ms        | 146           | 107       | 678                |
| 64 ms       | 15.5          | 14        | 78                 |
| 250 ms      | 4.1           | 5.7       | 21                 |

**It defaults to 0 because 64 ms made the gyro stutter.** Reported on a ROG Ally and reproduced on
an MSI Claw 8 AI+, and pinned to this setting alone by a device-level A/B: at 6 ms the stutter goes,
at 64 ms it comes back, with WSGM, Steam and everything else unchanged (2026-09-26).

The mechanism: Steam integrates the Deck's gyro per report, as SDL's Deck driver does, advancing its
sensor clock a fixed step per packet, so a `bInterval` without a report is motion that never
happens. A moving gyro whose encoded value briefly repeats counts as idle to this timer, and the
client skips frames that did not change, so the pause lands in the middle of a movement. "Fresh
input never waits for either timer" was true and beside the point, and the harness that measured
the same completion cadence at both settings was counting completions rather than looking at when
they landed. Do not raise this default for a device whose consumer streams motion.

## Completions on the poll grid

A paced endpoint completes each URB at its poll time with whatever state it has then. Fresh input
that landed since the last poll goes out at once; input that lands after a poll rides the next one,
at most one `bInterval` later, which is the latency of the hardware being emulated.

Until 2026-09-26 the worker paced a URB to the `bInterval` and then waited up to another `bInterval`
for fresh input, completing the moment it arrived. That put the report stream on the consumer's
sample cadence instead of the endpoint's, and when the two do not divide, the host sees a held
report and a fresh one a fraction of a millisecond apart every few samples. Measured with a raw HID
handle on the virtual Deck next to live Steam, with a 125 Hz gyro on the 6 ms endpoint: 60 % of the
gaps were 8 ms, a quarter of all reports arrived under 3 ms after the previous one, and those
carried the packet number the host had already seen. That was the gyro microstutter. It had been
hidden while the client submitted every pad report unconditionally, because a signal was then
pending at nearly every poll; skipping unchanged frames exposed it. `TestCompletionsStayOnTheEndpointGrid`
in `internal/server/usb` covers the cadence and the counter.

## Data-driven completions

A device that implements `usb.InterruptInSource` is served without a blocking call per poll. The
server owns the endpoint's poll timer and its completion frame: it waits on the device's own input
channel and a timer it reuses, and completes each URB by having the device encode its current state
into that frame. The encode is called once per report on the wire, changed state or not, so the
Steam Deck advances its packet number there, the way its firmware numbers every report it sends.

That removes, per completion, a context with a deadline, a context with a cancel, a report
allocation inside the device and the copy into the server's replay cache. On the same machine an
idle Steam Deck endpoint went from 23 heap allocations per completion to 1, and from 1,324 to 735
thousand cycles per completion. Devices that do not implement the interface keep the previous path,
one `HandleTransfer` call per URB with a context per URB, and still get the idle repeat pacing.

The Steam Deck implements it. Its keyboard and mouse endpoints return a nil input channel, which is
how a placeholder endpoint says it has nothing of its own to send.

## GOMAXPROCS

`libviiper` caps `GOMAXPROCS` at 4 on machines with more cores, unless `VIIPER_GOMAXPROCS` says
otherwise. Pinning it to 1 measured better — the work per URB is microseconds, and with more than
one P every URB is handed from the reading goroutine to its endpoint worker across OS threads:
three more context switches per completion and about a third more CPU.

That pin was reverted anyway. It was the first suspect for the gyro stutter, because a caller's
input update arrives over cgo from a thread of its own and has to acquire the single P — the one
thing the harness cannot model, since its producer is an in-runtime goroutine. The device said
otherwise: with the cap back at 4 the stutter stayed, and it was the idle repeat all along. An
unexplained scheduling change on the input path is not worth an idle saving.

## Validation status

The library was compiled and measured with the harness above, and the two pacing changes were then
A/B tested one at a time on an MSI Claw 8 AI+ against live Steam, which is what found the stutter
and told the two apart. The grid completion was then measured the same way, by timestamping the
reports a raw HID handle receives from the virtual Deck next to Steam while the device moves.
Rumble is still the maintainer's manual check.

Take the harness numbers for what they are: allocation and cycle counts per completion, measured
against a test client. They did not predict what a real host and Steam do with a slowed report
stream, and on the one occasion that mattered they pointed the wrong way. What the consumer sees is
the timing of each report, and that is what to record.
