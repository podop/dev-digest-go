package domain_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/ci/domain"
)

func TestRunnerEncodingRoundTrip(t *testing.T) {
	exe := bytes.Repeat([]byte("\x7fELF runner bytes \x00\x01\x02 "), 5000)
	enc, err := domain.EncodeRunner(exe)
	if err != nil {
		t.Fatalf("EncodeRunner() error = %v", err)
	}
	if !strings.HasSuffix(enc, "\n") || strings.Count(enc, "\n") != 1 {
		t.Errorf("EncodeRunner() = %d newlines, want exactly one trailing newline", strings.Count(enc, "\n"))
	}
	again, err := domain.EncodeRunner(exe)
	if err != nil || again != enc {
		t.Errorf("EncodeRunner() is not deterministic (err = %v)", err)
	}
	got, err := domain.DecodeRunner(enc, int64(len(exe)))
	if err != nil {
		t.Fatalf("DecodeRunner() error = %v", err)
	}
	if !bytes.Equal(got, exe) {
		t.Errorf("DecodeRunner(EncodeRunner(exe)) differs from exe")
	}
}

func TestDecodeRunner_Errors(t *testing.T) {
	enc, err := domain.EncodeRunner([]byte("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, in string
		max      int64
	}{
		{"not base64", "%%%\n", 100},
		{"not gzip", "aGVsbG8=\n", 100},
		{"over the cap", enc, 9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := domain.DecodeRunner(tc.in, tc.max); !errors.Is(err, domain.ErrRunnerEncoding) {
				t.Errorf("DecodeRunner(%q, %d) error = %v, want ErrRunnerEncoding", tc.in, tc.max, err)
			}
		})
	}
	if _, err := domain.DecodeRunner(enc, 10); err != nil {
		t.Errorf("DecodeRunner(enc, 10) error = %v, want nil (exactly at the cap)", err)
	}
}

func TestRunnerSHA256AndSumFile(t *testing.T) {
	// echo -n abc | sha256sum
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := domain.RunnerSHA256([]byte("abc")); got != abc {
		t.Errorf("RunnerSHA256(abc) = %q, want %q", got, abc)
	}
	if got, want := domain.RunnerSumFile(abc), abc+"  devdigest-runner\n"; got != want {
		t.Errorf("RunnerSumFile() = %q, want %q", got, want)
	}
}
