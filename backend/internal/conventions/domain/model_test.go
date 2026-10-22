package domain_test

import (
	"testing"

	"dev-digest-go/backend/internal/conventions/domain"
)

func TestIsBlank(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", true},
		{"spaces and newlines", " \n\t\r\n ", true},
		{"no-break space and BOM", "\u00a0\uFEFF\u2003", true},
		{"vertical tab and form feed", "\v\f", true},
		{"a letter", "  x ", false},
		{"NEL is not JavaScript white space", "\u0085", false},
		{"zero width space is not white space", "​", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.IsBlank(tc.text); got != tc.want {
				t.Errorf("IsBlank(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestConventionDecided(t *testing.T) {
	tests := []struct {
		name string
		c    domain.Convention
		want bool
	}{
		{"pending unedited", domain.Convention{Status: domain.StatusPending}, false},
		{"pending edited", domain.Convention{Status: domain.StatusPending, Edited: true}, true},
		{"accepted", domain.Convention{Status: domain.StatusAccepted}, true},
		{"rejected", domain.Convention{Status: domain.StatusRejected}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.Decided(); got != tc.want {
				t.Errorf("Decided() = %v, want %v", got, tc.want)
			}
		})
	}
}
