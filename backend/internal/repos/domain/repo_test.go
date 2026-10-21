package domain_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repos/domain"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want domain.Ref
	}{
		{name: "https", in: "https://github.com/o/r", want: domain.Ref{Owner: "o", Name: "r"}},
		{name: "https .git", in: "https://github.com/o/r.git", want: domain.Ref{Owner: "o", Name: "r"}},
		{name: "https trailing slash", in: "https://github.com/o/r/", want: domain.Ref{Owner: "o", Name: "r"}},
		{name: "https .git and slash", in: "https://github.com/o/r.git/", want: domain.Ref{Owner: "o", Name: "r"}},
		{name: "trimmed", in: " \thttps://github.com/o/r.git \n", want: domain.Ref{Owner: "o", Name: "r"}},
		{name: "ssh", in: "git@github.com:o/r", want: domain.Ref{Owner: "o", Name: "r"}},
		{name: "ssh .git", in: "git@github.com:o/r.git", want: domain.Ref{Owner: "o", Name: "r"}},
		{name: "case kept", in: "https://github.com/Acme/Pay-API_v2.js", want: domain.Ref{Owner: "Acme", Name: "Pay-API_v2.js"}},
		{name: "dots inside", in: "https://github.com/a.b/c..d", want: domain.Ref{Owner: "a.b", Name: "c..d"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ParseURL(tc.in)
			if err != nil {
				t.Fatalf("ParseURL(%q) error = %v", tc.in, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseURL(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestParseURL_Rejects(t *testing.T) {
	for _, in := range []string{
		"", "   ",
		"http://github.com/o/r",
		"ssh://git@github.com/o/r",
		"https://github.com/o/r/tree/main",
		"https://gitlab.com/o/r",
		"https://github.com.evil.com/o/r",
		"https://github.com/o",
		"https://github.com//r",
		"https://github.com/../r",
		"https://github.com/o/..",
		"https://github.com/./r",
		"https://github.com/o/.git",
		"https://github.com/.GIT/r",
		"https://github.com/-x/r",
		"https://github.com/o/-r",
		"git@github.com:o/r/extra",
		"git@github.com:-x/r",
		"git@gitlab.com:o/r",
		"https://github.com/o/r?x=1",
		"prefix https://github.com/o/r",
	} {
		t.Run(in, func(t *testing.T) {
			if got, err := domain.ParseURL(in); !errors.Is(err, domain.ErrInvalidURL) {
				t.Errorf("ParseURL(%q) = %+v, %v, want ErrInvalidURL", in, got, err)
			}
		})
	}
}

func TestRef(t *testing.T) {
	r := domain.Ref{Owner: "o", Name: "r"}
	if got, want := r.FullName(), "o/r"; got != want {
		t.Errorf("FullName() = %q, want %q", got, want)
	}
	if got, want := r.CloneURL("https://github.com/"), "https://github.com/o/r.git"; got != want {
		t.Errorf("CloneURL() = %q, want %q", got, want)
	}
}

func TestRepo_Cloned(t *testing.T) {
	path := "/data/o/r"
	if (domain.Repo{}).Cloned() {
		t.Error("Cloned() = true for a repo without clone path")
	}
	if !(domain.Repo{ClonePath: &path}).Cloned() {
		t.Error("Cloned() = false for a repo with clone path")
	}
}
