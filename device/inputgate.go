package device

import (
	"context"
	"errors"
)

// InputGate wakes a blocked interrupt-IN HandleTransfer when fresh input is
// available. Ported concept from upstream 7e33d2d3 ("Improve device emulation
// efficiency"): instead of completing every IN URB immediately with
// possibly-duplicate state, devices block until input actually changed (or
// the endpoint's bInterval deadline supplied by the server via ctx) — which
// cuts duplicate-report churn and ships fresh input the moment it arrives.
//
// The gate is a coalescing 1-buffered signal (latest-wins): any number of
// Signal calls between two waits collapse into one wake-up, and the report is
// built from the device's CURRENT state at wake time.
type InputGate struct {
	ch chan struct{}
}

func NewInputGate() *InputGate {
	return &InputGate{ch: make(chan struct{}, 1)}
}

// Signal marks fresh input available. Never blocks.
func (g *InputGate) Signal() {
	if g == nil {
		return
	}
	select {
	case g.ch <- struct{}{}:
	default:
	}
}

// C returns the gate's coalescing signal channel, for a caller that selects on it together with
// its own timer instead of calling Wait. A nil gate has no channel, and a receive on the nil
// channel blocks, which is what a device without input wants.
func (g *InputGate) C() <-chan struct{} {
	if g == nil {
		return nil
	}
	return g.ch
}

// GateResult is the outcome of waiting for input.
type GateResult int

const (
	// GateFresh: new input arrived — build and send a report.
	GateFresh GateResult = iota
	// GateDeadline: the endpoint poll interval elapsed with no new input —
	// build a report from current state anyway (host keepalive at poll rate).
	GateDeadline
	// GateCancelled: the URB/stream was cancelled — send nothing.
	GateCancelled
)

// Wait blocks until fresh input is signalled or ctx ends. It distinguishes a
// poll-interval deadline (send current state) from a real cancellation (send
// nothing), matching upstream's per-device select.
func (g *InputGate) Wait(ctx context.Context) GateResult {
	if g == nil {
		return GateFresh
	}
	select {
	case <-g.ch:
		return GateFresh
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return GateDeadline
		}
		return GateCancelled
	}
}

// BlockUntilDeadline blocks until ctx ends without consuming a gate signal.
// Used by SECONDARY IN endpoints (static keyboard/mouse reports, bulk/auth
// queues) so they complete at their poll interval instead of resubmitting in
// a tight loop, while leaving the fresh-input signal for the primary
// controller endpoint. Returns GateDeadline on the poll timeout (send the
// static/queued report) or GateCancelled on teardown (send nothing).
func BlockUntilDeadline(ctx context.Context) GateResult {
	<-ctx.Done()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return GateDeadline
	}
	return GateCancelled
}
