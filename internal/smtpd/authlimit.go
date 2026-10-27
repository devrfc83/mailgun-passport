package smtpd

import (
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// authBlockTime is how long a client address stays blocked after its last
// failed AUTH, and how long failures are remembered.
const authBlockTime = 15 * time.Minute

// authLimitLoopback makes the limiter count loopback clients too; tests set it.
var authLimitLoopback = false

// authLimiter counts failed AUTH attempts per client address across
// connections, so opening more connections does not buy more guesses. The
// per-session limit still applies on top of it.
//
// Loopback clients are not counted: every local application shares
// 127.0.0.1, and one with a wrong password would lock out all the others.
type authLimiter struct {
	max int
	log *slog.Logger
	now func() time.Time

	mu        sync.Mutex
	clients   map[netip.Prefix]*authFailures
	nextSweep int
}

type authFailures struct {
	count int
	last  time.Time
}

// newAuthLimiter returns nil, which never blocks, when max is 0.
func newAuthLimiter(max int, log *slog.Logger) *authLimiter {
	if max <= 0 {
		return nil
	}
	return &authLimiter{
		max:       max,
		log:       log,
		now:       time.Now,
		clients:   make(map[netip.Prefix]*authFailures),
		nextSweep: 1024,
	}
}

// authClient returns the key failures are counted under: the IPv4 address,
// or the /64 an IPv6 address belongs to, since a single host usually has a
// whole /64. ok is false for clients that are not limited.
func authClient(addr net.Addr) (key netip.Prefix, ok bool) {
	tcp, isTCP := addr.(*net.TCPAddr)
	if !isTCP {
		return netip.Prefix{}, false
	}
	ip, valid := netip.AddrFromSlice(tcp.IP)
	if !valid {
		return netip.Prefix{}, false
	}
	ip = ip.Unmap()
	if ip.IsLoopback() && !authLimitLoopback {
		return netip.Prefix{}, false
	}
	bits := 32
	if ip.Is6() {
		bits = 64
	}
	key, err := ip.Prefix(bits)
	return key, err == nil
}

// blocked reports whether client has reached the limit within authBlockTime.
func (l *authLimiter) blocked(client netip.Prefix) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.clients[client]
	return f != nil && f.count >= l.max && l.now().Sub(f.last) < authBlockTime
}

// fail records a failed AUTH from client.
func (l *authLimiter) fail(client netip.Prefix) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	f := l.clients[client]
	if f == nil {
		l.sweep(now)
		f = &authFailures{}
		l.clients[client] = f
	} else if now.Sub(f.last) >= authBlockTime {
		f.count = 0
	}
	f.count++
	f.last = now
	if f.count == l.max {
		l.log.Warn("blocking SMTP authentication: SMTP_AUTH_MAX_FAILURES reached",
			"client", client.String(), "failures", f.count, "for", authBlockTime)
	}
}

// sweep forgets expired clients once the map has doubled since the last
// sweep, so addresses that failed once do not accumulate forever.
func (l *authLimiter) sweep(now time.Time) {
	if len(l.clients) < l.nextSweep {
		return
	}
	for k, f := range l.clients {
		if now.Sub(f.last) >= authBlockTime {
			delete(l.clients, k)
		}
	}
	l.nextSweep = max(2*len(l.clients), 1024)
}
