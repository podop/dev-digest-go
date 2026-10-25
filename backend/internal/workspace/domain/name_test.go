package domain_test

import (
	"errors"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/workspace/domain"
)

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "plain", in: "acme", want: "acme"},
		{name: "surrounding spaces are trimmed (EC1)", in: "  acme · payments ", want: "acme · payments"},
		{name: "inner spaces kept", in: "a  b", want: "a  b"},
		{name: "only spaces", in: "   ", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "80 characters", in: strings.Repeat("a", 80), want: strings.Repeat("a", 80)},
		{name: "81 characters (EC2)", in: strings.Repeat("a", 81), wantErr: true},
		{name: "80 code points of 4 bytes", in: strings.Repeat("😀", 80), want: strings.Repeat("😀", 80)},
		{name: "81 code points of 4 bytes", in: strings.Repeat("😀", 81), wantErr: true},
		{name: "trimmed length counts, not raw", in: " " + strings.Repeat("a", 80) + " ", want: strings.Repeat("a", 80)},
		{name: "inner newline (EC2)", in: "a\nb", wantErr: true},
		{name: "inner tab (EC2)", in: "a\tb", wantErr: true},
		{name: "NUL", in: "a\x00b", wantErr: true},
		{name: "unit separator U+001F", in: "a\x1fb", wantErr: true},
		{name: "DEL U+007F", in: "a\x7fb", wantErr: true},
		{name: "U+0080 is not a control character here", in: "a\u0080b", want: "a\u0080b"},
		{name: "invalid UTF-8", in: "a\xffb", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.NormalizeName(tc.in)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalidName) {
					t.Fatalf("NormalizeName(%q) error = %v, want ErrInvalidName", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeName(%q) error = %v, want nil", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
