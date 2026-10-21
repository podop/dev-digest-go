package httpx

import "testing"

func TestHostName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"localhost", "localhost"},
		{"localhost:3001", "localhost"},
		{"LocalHost:3001", "localhost"},
		{"127.0.0.1:3101", "127.0.0.1"},
		{"[::1]", "[::1]"},
		{"[::1]:3001", "[::1]"},
		{"[::1", ""},
		{"[::1]x", ""},
		{"::1", ""},
		{"backend.", "backend"},
		{" backend ", "backend"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := hostName(tc.in); got != tc.want {
			t.Errorf("hostName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
