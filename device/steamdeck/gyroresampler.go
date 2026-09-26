package steamdeck

import (
	"math"
	"time"
)

// minReportInterval is the shortest gap between two reports over which a mean rate is formed. Two
// reports closer than this, which a host produces only around an unlink, both carry the current
// rate instead.
const minReportInterval = 500 * time.Microsecond

// gyroResampler turns the client's gyro samples, which arrive at the sensor's own cadence, into
// the mean angular rate over each poll interval.
//
// Steam integrates the Deck's gyro per report with a fixed time step, so the total rotation it
// sees is the sum of the reported rates. Copying the latest sample into each report makes that
// sum depend on how the two cadences line up: a 125 Hz sensor on the 6 ms endpoint puts every
// fourth sample into two reports, a 25 % velocity ripple at 42 Hz that reads as a microstutter
// while panning (MSI Claw, 2026-09-26). Reporting the mean rate since the previous report makes
// the sum exact for any sensor cadence and any poll cadence, and costs one multiply per axis per
// sample and per report. A sample is held until the next one arrives, which is what the sensor
// itself does between readings.
type gyroResampler struct {
	// rate is the sample in effect since at, in report counts.
	rate [3]float64
	// angle is the integral of rate since the previous report, in counts times seconds.
	angle [3]float64
	// at is the time angle is valid for; the zero value means no sample has arrived.
	at time.Time
	// reportedAt is the time of the previous report; the zero value means none yet.
	reportedAt time.Time
}

// advance integrates the current rate up to now.
func (r *gyroResampler) advance(now time.Time) {
	if !r.at.IsZero() {
		if dt := now.Sub(r.at).Seconds(); dt > 0 {
			for i := range r.rate {
				r.angle[i] += r.rate[i] * dt
			}
		}
	}
	r.at = now
}

// update records a fresh sample: the previous rate held until now, the new one holds from now on.
func (r *gyroResampler) update(now time.Time, x, y, z int16) {
	r.advance(now)
	r.rate = [3]float64{float64(x), float64(y), float64(z)}
}

// report returns the rate to put in a report sent now: the mean rate since the previous report.
// The first report, and one that follows the previous within minReportInterval, carries the
// current rate.
func (r *gyroResampler) report(now time.Time) (x, y, z int16) {
	r.advance(now)
	out := r.rate
	if !r.reportedAt.IsZero() {
		if dt := now.Sub(r.reportedAt).Seconds(); dt >= minReportInterval.Seconds() {
			for i := range out {
				out[i] = r.angle[i] / dt
			}
		}
	}
	r.angle = [3]float64{}
	r.reportedAt = now
	return clampCounts(out[0]), clampCounts(out[1]), clampCounts(out[2])
}

// reset forgets every sample, for a client that cleared the input state.
func (r *gyroResampler) reset() {
	*r = gyroResampler{}
}

func clampCounts(v float64) int16 {
	v = math.Round(v)
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}
