// Package throttle provides byte-per-second rate limiting for file transfers.
//
// The agent uses it to cap FetchContent (server to agent) and UploadContent
// (agent to server) streams at a bytes-per-second budget. The rate can come
// from either the agent's config (admin-wide cap) or a signed command field
// (per-task operator rate); the caller applies BigFix "lower wins" semantics
// via EffectiveRate() before wrapping the stream.
//
// Design (simple sliding window):
//   - Track start_time + bytes_transferred for the lifetime of the reader/writer
//   - On each I/O, compute expected_time = bytes_transferred / rate
//   - If actual elapsed time is less than expected, sleep the difference
//
// This is NOT a token bucket (no burst allowance). It is a smooth rate limiter
// that keeps the overall stream rate near the configured bytes-per-second over
// the lifetime of the wrapper. For agent transfers this matches what the admin
// usually wants: a predictable "stays below N MB/s" budget, not a bursty one.
//
// Zero or negative rate = passthrough (no throttling, no overhead).
package throttle

import (
	"io"
	"time"
)

// EffectiveRate picks the effective bytes-per-second cap for one transfer by
// applying BigFix "lower wins" semantics across the available rate sources:
//
//   - agentCap: the admin-wide endpoint cap (from agent config)
//   - signedRate: the per-task rate from the signed command (if any)
//
// Treating zero as unlimited means a per-task "no limit" value (0) does not
// override the admin cap, and an unset admin cap does not override the
// per-task value. Negative values are treated the same as zero.
func EffectiveRate(agentCap, signedRate int64) int64 {
	if agentCap < 0 {
		agentCap = 0
	}
	if signedRate < 0 {
		signedRate = 0
	}
	switch {
	case agentCap == 0 && signedRate == 0:
		return 0
	case agentCap == 0:
		return signedRate
	case signedRate == 0:
		return agentCap
	}
	if agentCap < signedRate {
		return agentCap
	}
	return signedRate
}

// NewReader wraps r so reads never exceed bytesPerSecond on average. A rate
// of zero or less returns r unchanged (zero-overhead passthrough).
func NewReader(r io.Reader, bytesPerSecond int64) io.Reader {
	if bytesPerSecond <= 0 {
		return r
	}
	return &throttledReader{r: r, rate: bytesPerSecond, start: time.Now(), now: time.Now, sleep: time.Sleep}
}

// NewWriter wraps w so writes never exceed bytesPerSecond on average. A rate
// of zero or less returns w unchanged.
func NewWriter(w io.Writer, bytesPerSecond int64) io.Writer {
	if bytesPerSecond <= 0 {
		return w
	}
	return &throttledWriter{w: w, rate: bytesPerSecond, start: time.Now(), now: time.Now, sleep: time.Sleep}
}

// throttledReader enforces a byte-per-second cap on reads via a sliding window.
// The clock (now) and sleep are injectable so tests can advance virtual time
// without a real wall-clock delay; production uses time.Now and time.Sleep.
type throttledReader struct {
	r     io.Reader
	rate  int64 // bytes per second
	start time.Time
	total int64 // bytes read so far
	now   func() time.Time
	sleep func(time.Duration)
}

func (t *throttledReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.total += int64(n)
		t.pace()
	}
	return n, err
}

func (t *throttledReader) pace() {
	expected := time.Duration(float64(t.total) / float64(t.rate) * float64(time.Second))
	elapsed := t.now().Sub(t.start)
	if expected > elapsed {
		t.sleep(expected - elapsed)
	}
}

// throttledWriter enforces a byte-per-second cap on writes via a sliding window.
type throttledWriter struct {
	w     io.Writer
	rate  int64
	start time.Time
	total int64
	now   func() time.Time
	sleep func(time.Duration)
}

func (t *throttledWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if n > 0 {
		t.total += int64(n)
		t.pace()
	}
	return n, err
}

func (t *throttledWriter) pace() {
	expected := time.Duration(float64(t.total) / float64(t.rate) * float64(time.Second))
	elapsed := t.now().Sub(t.start)
	if expected > elapsed {
		t.sleep(expected - elapsed)
	}
}
