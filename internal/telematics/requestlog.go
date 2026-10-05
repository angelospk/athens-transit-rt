package telematics

import (
	"sync"
	"time"
)

// RequestLogSeconds is how far back RequestLog remembers.
const RequestLogSeconds = 3600

// RequestLog counts requests per wall-clock second over the last hour (for the local
// metrics endpoint: budget and spacing checks).
type RequestLog struct {
	mu    sync.Mutex
	count [RequestLogSeconds]int32
	sec   [RequestLogSeconds]int64 // which Unix second each slot holds
}

func (l *RequestLog) Add(t time.Time) {
	s := t.Unix()
	i := s % RequestLogSeconds
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sec[i] != s {
		l.sec[i], l.count[i] = s, 0
	}
	l.count[i]++
}

// PerSecond returns the counts of the n seconds ending with now's second, oldest first.
func (l *RequestLog) PerSecond(now time.Time, n int) []int32 {
	n = min(n, RequestLogSeconds)
	out := make([]int32, n)
	end := now.Unix()
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := 0; k < n; k++ {
		s := end - int64(n-1-k)
		if i := s % RequestLogSeconds; l.sec[i] == s {
			out[k] = l.count[i]
		}
	}
	return out
}

// MaxWindow is the largest sum over any `width` consecutive entries.
func MaxWindow(per []int32, width int) int32 {
	var sum, best int32
	for i, v := range per {
		sum += v
		if i >= width {
			sum -= per[i-width]
		}
		best = max(best, sum)
	}
	return best
}
