package app

import "testing"

func TestIsFullPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{name: "empty", pattern: "", want: true},
		{name: "blank", pattern: " \t\n", want: true},
		{name: "star", pattern: "*", want: true},
		{name: "star with spaces", pattern: " * ", want: true},
		{name: "prefix glob", pattern: "user*", want: false},
		{name: "contains star", pattern: "user:*", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFullPattern(tt.pattern); got != tt.want {
				t.Fatalf("isFullPattern(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}
