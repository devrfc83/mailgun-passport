package smtpd

import (
	"net/netip"
	"sync"
	"time"
)

// rateLimiter allows each client address a number of messages per minute, with
// a burst of the same size. It is a token bucket per address, keyed like the
// AUTH limiter (an IPv6 client counts per /64), except that loopback clients
// are counted too: for applications on the relay's own host, the limit is
// effectively global, which is how a runaway local application is stopped.
type rateLimiter struct {
	perMinute float64
	now       func() time.Time

	mu        sync.Mutex
	buckets   map[netip.Prefix]*bucket
	nextSweep int
}

type bucket struct {
	tokens float64
	last   time.Time
}

// newRateLimiter returns nil, which allows everything, when perMinute is 0.
func newRateLimiter(perMinute int) *rateLimiter {
	if perMinute <= 0 {
		return nil
	}
	return &rateLimiter{
		perMinute: float64(perMinute),
		now:       time.Now,
		buckets:   map[netip.Prefix]*bucket{},
		nextSweep: 1024,
	}
}

// allow takes one message from client's allowance and reports whether it had one.
func (l *rateLimiter) allow(client netip.Prefix) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[client]
	if b == nil {
		l.sweep(now)
		b = &bucket{tokens: l.perMinute, last: now}
		l.buckets[client] = b
	} else {
		b.tokens = min(l.perMinute, b.tokens+now.Sub(b.last).Minutes()*l.perMinute)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep forgets clients whose allowance has refilled completely, once the map
// has doubled since the last sweep. A full bucket is what a new client gets,
// so nothing is lost.
func (l *rateLimiter) sweep(now time.Time) {
	if len(l.buckets) < l.nextSweep {
		return
	}
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Minutes()*l.perMinute >= l.perMinute {
			delete(l.buckets, k)
		}
	}
	l.nextSweep = max(2*len(l.buckets), 1024)
}
