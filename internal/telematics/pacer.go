package telematics

import (
	"context"
	"sync"
	"time"
)

const (
	DefaultRPS  = 4
	MaxRPS      = 5
	maxBackoff  = 8   // slowest pace = base interval x 8
	recoverRate = 0.9 // each healthy answer shrinks the backoff by this factor
	SlowAnswer  = 5 * time.Second
)

// Pacer spaces requests evenly: one start every interval, never a burst after idle time.
// Errors and slow answers stretch the interval (x2 each, up to x8); healthy answers shrink
// it back gradually.
type Pacer struct {
	mu      sync.Mutex
	base    time.Duration
	backoff float64
	next    time.Time
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

// RPS is the current effective request rate.
func (p *Pacer) RPS() float64 { return float64(time.Second) / float64(p.Interval()) }

// BaseRPS is the configured request rate.
func (p *Pacer) BaseRPS() float64 { return float64(time.Second) / float64(p.base) }

// reserve books the next slot at or after now and returns its start time.
func (p *Pacer) reserve(now time.Time) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	slot := p.next
	if slot.Before(now) {
		slot = now
	}
	p.next = slot.Add(p.interval())
	return slot
}

// Wait blocks until the caller's slot. A cancelled context returns its error (the slot is
// still consumed, which only makes the pace gentler).
func (p *Pacer) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d := time.Until(p.reserve(time.Now()))
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Report feeds the outcome of one request back into the pace.
func (p *Pacer) Report(latency time.Duration, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil || latency > SlowAnswer {
		p.backoff = min(p.backoff*2, maxBackoff)
	} else {
		p.backoff = max(p.backoff*recoverRate, 1)
	}
}
