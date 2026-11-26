// Package lan opens the build machine's local network to CRN's server before a real call
// needs it. macOS (Local Network privacy) fails a launchd program's first connection to a
// host on the local network with "no route to host" while it checks the program's
// permission, and lets the later ones through (26 Nov 2026: the AI Router's first calls
// after a restart, and since 5 Nov the first DV callback of six runs, which is not retried).
// Warm makes that first connection at start and says in the log whether the network is open.
package lan

import (
	"context"
	"log/slog"
	"net"
	"net/url"
	"time"
)

// Hosts is host:port of each http(s) address; empty or unreadable ones are left out.
func Hosts(urls ...string) []string {
	var out []string
	for _, s := range urls {
		u, err := url.Parse(s)
		if s == "" || err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
		}
		out = append(out, net.JoinHostPort(u.Hostname(), port))
	}
	return out
}

// Warm connects to the hosts in turn, up to tries rounds wait apart, until one answers.
// It reports whether one did; the connection is closed at once and nothing is sent.
func Warm(ctx context.Context, logger *slog.Logger, addrs []string, tries int, wait time.Duration) bool {
	if len(addrs) == 0 {
		return true
	}
	var last error
	d := net.Dialer{Timeout: 3 * time.Second}
	start := time.Now()
	for i := 1; i <= tries; i++ {
		for _, a := range addrs {
			c, err := d.DialContext(ctx, "tcp", a)
			if err == nil {
				c.Close()
				logger.Info("local network open", "host", a, "try", i, "after", time.Since(start).Round(time.Second).String())
				return true
			}
			last = err
		}
		if i == 1 && tries > 1 {
			logger.Warn("local network not open yet; trying again", "hosts", addrs, "every", wait.String(), "err", last.Error())
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
	}
	logger.Warn("local network shut to crn-server: if macOS refuses it, allow crn-server in System Settings › Privacy & Security › Local Network",
		"hosts", addrs, "err", last.Error())
	return false
}
