package steamdeck

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

// deckClock drives a device from a fake clock. The base is well away from the zero time, which
// the resampler uses to mean "never".
type deckClock struct {
	d   *SteamDeck
	now time.Time
	buf []byte
}

func newDeckClock(t *testing.T) *deckClock {
	t.Helper()
	d, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &deckClock{d: d, now: time.Unix(1_000_000, 0), buf: make([]byte, InputReportLen)}
	d.now = func() time.Time { return c.now }
	return c
}

func (c *deckClock) sample(atMs float64, pitch, yaw, roll int16) {
	c.now = time.Unix(1_000_000, 0).Add(time.Duration(atMs * float64(time.Millisecond)))
	c.d.UpdateInputState(&InputState{Pitch: pitch, Yaw: yaw, Roll: roll})
}

func (c *deckClock) report(atMs float64) (pitch, yaw, roll int16) {
	c.now = time.Unix(1_000_000, 0).Add(time.Duration(atMs * float64(time.Millisecond)))
	if _, ok := c.d.WriteInputReport(controllerEndpointNumber, c.buf); !ok {
		panic("controller report refused")
	}
	return int16(binary.LittleEndian.Uint16(c.buf[30:32])),
		int16(binary.LittleEndian.Uint16(c.buf[32:34])),
		int16(binary.LittleEndian.Uint16(c.buf[34:36]))
}

// A report carries the mean rate since the previous report, so a sample that changes in the
// middle of a poll interval is weighted by the time it was in effect.
func TestGyroReportsTheMeanRateSinceThePreviousReport(t *testing.T) {
	c := newDeckClock(t)
	c.sample(0, 1600, -800, 0)
	if p, y, _ := c.report(6); p != 1600 || y != -800 {
		t.Fatalf("first report = (%d, %d), want the current sample (1600, -800)", p, y)
	}
	c.sample(8, 0, 0, 0)
	// 2 ms at (1600, -800) and 4 ms at rest over the 6 ms window.
	if p, y, _ := c.report(12); p != 533 || y != -267 {
		t.Fatalf("report over a changing window = (%d, %d), want (533, -267)", p, y)
	}
	if p, y, _ := c.report(18); p != 0 || y != 0 {
		t.Fatalf("report at rest = (%d, %d), want (0, 0)", p, y)
	}
}

// The sum of the reported rates over the poll grid equals the rotation the samples described,
// whatever the ratio of the two cadences: here 8 ms samples on a 6 ms grid, the case that put
// every fourth sample into two reports when the latest sample was copied instead.
func TestGyroReportsConserveTheRotation(t *testing.T) {
	c := newDeckClock(t)
	const sampleMs, pollMs = 8.0, 6.0
	var described, reported float64
	var lastRate int16
	nextSample, nextPoll := 0.0, 6.0
	for step := 0; step < 200; step++ {
		if nextSample <= nextPoll {
			rate := int16(1000 * math.Sin(nextSample/40))
			c.sample(nextSample, rate, 0, 0)
			described += float64(lastRate) * sampleMs
			lastRate = rate
			nextSample += sampleMs
			continue
		}
		p, _, _ := c.report(nextPoll)
		reported += float64(p) * pollMs
		nextPoll += pollMs
	}
	// The described rotation counts each sample for the full interval it was held; the reports
	// stop at the last poll, so compare up to that point by closing the last sample there.
	described += float64(lastRate) * (nextPoll - pollMs - (nextSample - sampleMs))
	if diff := math.Abs(described - reported); diff > 0.01*math.Abs(described)+pollMs {
		t.Fatalf("reports integrate to %.0f counts·ms, samples described %.0f", reported, described)
	}
}

// Two reports within a hair of each other, which a host produces around an unlink, both carry
// the current rate rather than a rate formed over a vanishing interval.
func TestGyroReportsBackToBackCarryTheCurrentRate(t *testing.T) {
	c := newDeckClock(t)
	c.sample(0, 400, 0, 0)
	c.report(6)
	if p, _, _ := c.report(6.1); p != 400 {
		t.Fatalf("back-to-back report = %d, want 400", p)
	}
}
