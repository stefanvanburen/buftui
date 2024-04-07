package main

import (
	"testing"
	"time"
)

func Test_formatTimeAgo(t *testing.T) {
	t.Parallel()
	now := time.Date(2000, time.May, 31, 16, 39, 0, 0, time.Local)
	for _, tc := range []struct {
		timestamp time.Time
		want      string
	}{
		{
			timestamp: now.Add(time.Second),
			want:      "in the future",
		},
		{
			timestamp: now,
			want:      "now",
		},
		{
			timestamp: now.Add(-time.Second),
			want:      "a few seconds ago",
		},
		{
			timestamp: now.Add(-529600 * time.Minute),
			want:      "last year",
		},
		{
			timestamp: now.Add(-2 * 529600 * time.Minute),
			want:      "2 years ago",
		},
		{
			timestamp: now.Add(-40 * 24 * time.Hour),
			want:      "last month",
		},
		{
			timestamp: now.Add(-70 * 24 * time.Hour),
			want:      "2 months ago",
		},
		{
			timestamp: now.Add(-39 * time.Minute),
			want:      "39 minutes ago",
		},
		{
			timestamp: now.Add(-5 * time.Hour),
			want:      "5 hours ago",
		},
	} {
		got := formatTimeAgo(now, tc.timestamp)
		if got != tc.want {
			t.Errorf("formatTimeAGo(%s, %s) = %s, want %s", now, tc.timestamp, got, tc.want)
		}
	}
}
