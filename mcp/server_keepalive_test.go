package mcp

import (
	"testing"
	"time"
)

// The fallback must never disarm the ping: a missing, non-numeric or <= 0
// MHR_SSE_KEEPALIVE means "default", not "no keepalive".
func TestKeepaliveFromEnvFallback(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"", defaultSSEKeepalive},                     // unset
		{"1", 1 * time.Second},                        // usable override
		{"30", 30 * time.Second},                      // usable override
		{"0", defaultSSEKeepalive},                    // zero is not "off"
		{"-3", defaultSSEKeepalive},                   // negative is not "off"
		{"abc", defaultSSEKeepalive},                  // non-numeric
		{"1.5", defaultSSEKeepalive},                  // fractional
		{" 2", defaultSSEKeepalive},                   // no trimming: a space is invalid
		{"99999999999999999999", defaultSSEKeepalive}, // overflow
	}
	for _, c := range cases {
		t.Setenv("MHR_SSE_KEEPALIVE", c.value)
		if got := keepaliveFromEnv(); got != c.want {
			t.Errorf("MHR_SSE_KEEPALIVE=%q: got %s, want %s", c.value, got, c.want)
		}
	}
}
