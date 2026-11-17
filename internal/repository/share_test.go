package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The share-link lifecycle has two rules that both dialects must obey, and they
// live in this package precisely so there is one answer rather than two
// implementations that can drift.

func TestRedactShareToken(t *testing.T) {
	full := "0123456789abcdef0123456789abcdef"
	got := RedactShareToken(full)

	assert.Equal(t, "01234567…", got)
	assert.NotContains(t, got, full[ShareTokenPrefixLen:], "nothing past the prefix may survive")
	assert.Len(t, got, ShareTokenPrefixLen+len("…"),
		"the redaction must be a fixed shape so it cannot be mistaken for a whole token")

	// Two different links must still be distinguishable in a list.
	assert.NotEqual(t, RedactShareToken(full), RedactShareToken("fedcba9876543210fedcba9876543210"))
}

// TestRedactShareToken_ShortTokenIsNotRevealed guards the slicing boundary: a
// token no longer than the prefix would be returned whole by a naive
// token[:n], which is exactly the value this function exists to withhold.
func TestRedactShareToken_ShortTokenIsNotRevealed(t *testing.T) {
	// "" is in the list because it must not panic or come back as something
	// other than a mask; the containment check is skipped for it since every
	// string contains "".
	for _, token := range []string{"", "a", "abcdefgh"} {
		got := RedactShareToken(token)
		assert.Equal(t, "…", got)
		if token != "" {
			assert.NotContains(t, got, token, "a short token must not come back in the redaction")
		}
	}
}

func TestShareViewDue(t *testing.T) {
	window := 10 * time.Minute
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-window)
	recent := now.Add(-window + time.Second)
	future := now.Add(time.Hour)

	for _, tc := range []struct {
		name     string
		previous *time.Time
		want     bool
	}{
		{"never viewed", nil, true},
		{"exactly one window ago", &old, true},
		{"just inside the window", &recent, false},
		{"clock skew: stamped in the future", &future, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ShareViewDue(tc.previous, now, window))
		})
	}
}

// TestShareViewDue_ZeroWindowAlwaysWrites pins the escape hatch: a zero or
// negative window means "no bound", which is what a test or an operator asking
// for exact counting would pass. It must not panic or silently never write.
func TestShareViewDue_ZeroWindowAlwaysWrites(t *testing.T) {
	now := time.Now()
	recent := now
	assert.True(t, ShareViewDue(&recent, now, 0))
	assert.True(t, ShareViewDue(&recent, now, -time.Minute))
}
