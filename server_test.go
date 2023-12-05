package pot

import (
	"testing"
	"time"
)

func TestCanRewrite(t *testing.T) {
	cases := []struct {
		caseName         string
		lastModification time.Time
		now              time.Time
		duration         time.Duration
		expected         bool
	}{
		{"same time", time.Now(), time.Now(), time.Second, false},
		{"exact duration", time.Now(), time.Now().Add(time.Second), time.Second, true},
		{"possible rewrite", time.Now(), time.Now().Add(time.Second * 2), time.Second, true},
	}

	for _, c := range cases {
		t.Run(c.caseName, func(t *testing.T) {
			if canRewrite(c.lastModification, c.now, c.duration) != c.expected {
				t.Fatalf("expected %v, got %v", c.expected, !c.expected)
			}
		})
	}
}
