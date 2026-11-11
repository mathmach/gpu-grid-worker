package testkit

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func ServerPort(tb testing.TB, rawURL string) int {
	tb.Helper()
	hostPort := strings.TrimSpace(rawURL)
	if strings.Contains(hostPort, "://") {
		parsed, err := url.Parse(hostPort)
		if err != nil {
			tb.Fatalf("parse server url (%s): %v", rawURL, err)
		}
		hostPort = parsed.Host
	}
	_, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		tb.Fatalf("split host port (%s): %v", rawURL, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		tb.Fatalf("parse port (%s): %v", portStr, err)
	}
	return port
}

func WaitUntil(tb testing.TB, timeout time.Duration, desc string, check func() bool) {
	tb.Helper()
	if check() {
		return
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-ticker.C:
			if check() {
				return
			}
		case <-deadline.C:
			if check() {
				return
			}
			tb.Fatalf("timed out after %s waiting for %s", timeout, desc)
		}
	}
}
