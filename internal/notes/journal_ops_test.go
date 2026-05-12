package notes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestJournalPathSegments(t *testing.T) {
	tests := []struct {
		name        string
		when        time.Time
		wantYear    string
		wantMonth   string
		wantDay     string
	}{
		{
			name:      "tuesday in january",
			when:      time.Date(2025, time.January, 7, 12, 0, 0, 0, time.UTC),
			wantYear:  "2025",
			wantMonth: "01 - January",
			wantDay:   "07 - Tuesday",
		},
		{
			name:      "single-digit day padded",
			when:      time.Date(2025, time.March, 3, 0, 0, 0, 0, time.UTC),
			wantYear:  "2025",
			wantMonth: "03 - March",
			wantDay:   "03 - Monday",
		},
		{
			name:      "two-digit day not double-padded",
			when:      time.Date(2025, time.December, 31, 23, 59, 0, 0, time.UTC),
			wantYear:  "2025",
			wantMonth: "12 - December",
			wantDay:   "31 - Wednesday",
		},
		{
			name:      "leap day",
			when:      time.Date(2024, time.February, 29, 9, 0, 0, 0, time.UTC),
			wantYear:  "2024",
			wantMonth: "02 - February",
			wantDay:   "29 - Thursday",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			year, month, day := journalPathSegments(tc.when)
			assert.Equal(t, tc.wantYear, year)
			assert.Equal(t, tc.wantMonth, month)
			assert.Equal(t, tc.wantDay, day)
		})
	}
}
