package throttle

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestEffectiveRate(t *testing.T) {
	cases := []struct {
		name                string
		agentCap, signed    int64
		want                int64
	}{
		{"both zero", 0, 0, 0},
		{"only agent", 1000, 0, 1000},
		{"only signed", 0, 500, 500},
		{"signed lower", 1000, 500, 500},
		{"agent lower", 400, 500, 400},
		{"both equal", 500, 500, 500},
		{"negative treated as zero (agent)", -1, 500, 500},
		{"negative treated as zero (signed)", 400, -1, 400},
		{"both negative", -1, -1, 0},
	}
	for _, c := range cases {
		if got := EffectiveRate(c.agentCap, c.signed); got != c.want {
			t.Errorf("%s: EffectiveRate(%d, %d) = %d, want %d", c.name, c.agentCap, c.signed, got, c.want)
		}
	}
}

func TestNewReaderZeroRatePassthrough(t *testing.T) {
	src := bytes.NewReader(bytes.Repeat([]byte{0xAB}, 100))
	r := NewReader(src, 0)
	// Zero rate must return the underlying reader unchanged — same type, no
	// allocation of a throttledReader. This matters because production
	// transfers at effectiveRate == 0 should pay no runtime cost.
	if r != src {
		t.Fatalf("zero rate must passthrough underlying reader, got %T wrapping", r)
	}
	r = NewReader(src, -5)
	if r != src {
		t.Fatalf("negative rate must passthrough, got %T wrapping", r)
	}
}

func TestNewWriterZeroRatePassthrough(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, 0)
	if w != &buf {
		t.Fatalf("zero rate must passthrough underlying writer, got %T wrapping", w)
	}
}

// virtualClock lets a test advance "time" without blocking. Each sleep call
// advances the clock by the requested duration; now() returns the current
// virtual time. Mirrors how the production clock behaves (sleep moves the
// wall clock forward) so the pacing math under test matches reality.
type virtualClock struct {
	now   time.Time
	slept time.Duration
}

func (v *virtualClock) Now() time.Time         { return v.now }
func (v *virtualClock) Sleep(d time.Duration)  { v.now = v.now.Add(d); v.slept += d }

// TestThrottledReaderMatchesRate: 10 KB at 10 KB/s should take ~1 second of
// pacing sleep. Virtual clock advances with each simulated sleep so the
// pacing math behaves exactly as it would with a real wall clock, but the
// test completes instantly.
func TestThrottledReaderMatchesRate(t *testing.T) {
	const total = 10 * 1024
	const rate = 10 * 1024 // 10 KB/s -> ~1 second for 10 KB
	src := bytes.NewReader(bytes.Repeat([]byte{0x42}, total))
	tr := NewReader(src, rate).(*throttledReader)
	vc := &virtualClock{now: tr.start}
	tr.now = vc.Now
	tr.sleep = vc.Sleep

	buf := make([]byte, 1024)
	readTotal := 0
	for {
		n, err := tr.Read(buf)
		readTotal += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}
	}
	if readTotal != total {
		t.Fatalf("short read: got %d want %d", readTotal, total)
	}
	// Expected total pacing time ~= 1 second. The first chunk reads instantly
	// (window not yet elapsed), each subsequent chunk sleeps exactly 100 ms —
	// 9 chunks x 100 ms = 900 ms. Allow some slack on both sides.
	if vc.slept < 800*time.Millisecond {
		t.Fatalf("expected ~1s of pacing sleep for 10KB@10KB/s, got %s", vc.slept)
	}
	if vc.slept > 1200*time.Millisecond {
		t.Fatalf("over-paced: slept %s for 10KB@10KB/s", vc.slept)
	}
}

// TestThrottledWriterMatchesRate mirrors the reader test for symmetry.
func TestThrottledWriterMatchesRate(t *testing.T) {
	const total = 10 * 1024
	const rate = 10 * 1024
	var dst bytes.Buffer
	tw := NewWriter(&dst, rate).(*throttledWriter)
	vc := &virtualClock{now: tw.start}
	tw.now = vc.Now
	tw.sleep = vc.Sleep

	data := bytes.Repeat([]byte{0x24}, 1024)
	for written := 0; written < total; written += 1024 {
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}
	if dst.Len() != total {
		t.Fatalf("short write: got %d want %d", dst.Len(), total)
	}
	if vc.slept < 800*time.Millisecond || vc.slept > 1200*time.Millisecond {
		t.Fatalf("pacing out of range: slept %s for 10KB@10KB/s", vc.slept)
	}
}

// TestThrottledReaderRealClock: once end-to-end with real time.Sleep, verify
// the pipe actually took close to the expected wall-clock time. Short burst
// (1 KB at 2 KB/s = ~0.5s) so the test stays fast.
func TestThrottledReaderRealClock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-clock throttle test in short mode")
	}
	const total = 1024
	const rate = 2048 // 2 KB/s -> 0.5s
	src := bytes.NewReader(bytes.Repeat([]byte{0xFF}, total))
	tr := NewReader(src, rate)

	start := time.Now()
	if _, err := io.ReadAll(tr); err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 350*time.Millisecond {
		t.Fatalf("ran too fast: %s (expected ~500ms)", elapsed)
	}
	if elapsed > 900*time.Millisecond {
		t.Fatalf("ran too slow: %s (expected ~500ms)", elapsed)
	}
}

// TestThrottledReaderPropagatesError: wrapper must not swallow underlying errors.
func TestThrottledReaderPropagatesError(t *testing.T) {
	tr := NewReader(&errReader{err: io.ErrUnexpectedEOF}, 1024)
	buf := make([]byte, 10)
	n, err := tr.Read(buf)
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("expected ErrUnexpectedEOF, got %v (n=%d)", err, n)
	}
}

type errReader struct{ err error }

func (e *errReader) Read([]byte) (int, error) { return 0, e.err }
