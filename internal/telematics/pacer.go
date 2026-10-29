package telematics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	DefaultRPS  = 6
	MaxRPS      = 6
	maxBackoff  = 8   // slowest pace = base interval x 8
	recoverRate = 0.9 // each healthy answer shrinks the backoff by this factor
	SlowAnswer  = 5 * time.Second
	BlockPause  = 10 * time.Minute // no request at all after OASA answers 403 or 429
)

// Pacer spaces requests evenly: one grant every interval, never a burst after idle time.
// The interval counts from the actual grant, so a timer that wakes late never lets the next
// caller in early. Errors and slow answers stretch the interval (x2 each, up to x8); healthy
// answers shrink it back gradually. A 403 or 429 (OASA refusing us) stops every request for
// BlockPause, then the pace restarts at its slowest.
type Pacer struct {
	Log *slog.Logger // where a stop is logged (nil = not logged)

	mu      sync.Mutex
	base    time.Duration
	backoff float64
	last    time.Time // last grant
	stopped time.Time // no grant before this
}

// NewPacer clamps rps to (0, MaxRPS]; rps <= 0 means DefaultRPS.
func NewPacer(rps float64) *Pacer {
	if rps <= 0 {
		rps = DefaultRPS
	}
	rps = min(rps, MaxRPS)
	return &Pacer{base: time.Duration(float64(time.Second) / rps), backoff: 1}
}

// Interval is the current spacing between request starts.
func (p *Pacer) Interval() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.interval()
}

func (p *Pacer) interval() time.Duration { return time.Duration(float64(p.base) * p.backoff) }

// BaseInterval is the spacing at the configured rate (no backoff).
func (p *Pacer) BaseInterval() time.Duration { return p.base }

// RPS is the current effective request rate.
func (p *Pacer) RPS() float64 { return float64(time.Second) / float64(p.Interval()) }

// BaseRPS is the configured request rate.
func (p *Pacer) BaseRPS() float64 { return float64(time.Second) / float64(p.base) }

// grant takes the slot if one interval passed since the last grant (returns 0), else returns
// how long to wait before trying again.
func (p *Pacer) grant(now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if wait := p.stopped.Sub(now); wait > 0 {
		return wait
	}
	if wait := p.last.Add(p.interval()).Sub(now); !p.last.IsZero() && wait > 0 {
		return wait
	}
	p.last = now
	return 0
}

// Wait blocks until the caller holds a slot, or returns ctx's error.
func (p *Pacer) Wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		d := p.grant(time.Now())
		if d == 0 {
			return nil
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Report feeds the outcome of one request back into the pace.
func (p *Pacer) Report(latency time.Duration, err error) { p.report(time.Now(), latency, err) }

func (p *Pacer) report(now time.Time, latency time.Duration, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var he *HTTPError
	if errors.As(err, &he) && (he.Status == http.StatusForbidden || he.Status == http.StatusTooManyRequests) {
		p.backoff = maxBackoff
		if !now.Before(p.stopped) { // answers of requests sent before the stop do not extend it
			p.stopped = now.Add(BlockPause)
			if p.Log != nil {
				p.Log.Error("OASA refused requests; all OASA requests stopped", "status", he.Status,
					"act", he.Act, "until", p.stopped.UTC().Format(time.RFC3339))
			}
		}
		return
	}
	if now.Before(p.stopped) {
		return // answers of requests sent before the stop keep the restart at the slowest pace
	}
	if err != nil || latency > SlowAnswer {
		p.backoff = min(p.backoff*2, maxBackoff)
	} else {
		p.backoff = max(p.backoff*recoverRate, 1)
	}
}
