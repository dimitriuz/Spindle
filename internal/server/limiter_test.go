package server

import (
	"testing"
	"time"
)

func TestClientKeyDropsPort(t *testing.T) {
	if a, b := clientKey("192.168.1.9:50001"), clientKey("192.168.1.9:50002"); a != b {
		t.Fatalf("same host, different ephemeral port must share a bucket: %q %q", a, b)
	}
	if clientKey("[fe80::1]:80") != "fe80::1" {
		t.Fatalf("ipv6: %q", clientKey("[fe80::1]:80"))
	}
}

func TestLimiterEvictsIdleBuckets(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(20, 40, func() time.Time { return now })
	for i := 0; i < 600; i++ {
		l.Allow(string(rune('a'+i%26)) + time.Duration(i).String())
	}
	now = now.Add(2 * time.Minute)
	l.Allow("fresh")
	if len(l.m) > 2 {
		t.Fatalf("idle buckets must be swept, have %d", len(l.m))
	}
}
