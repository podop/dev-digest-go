package gitlab_test

import (
	"context"

	cigitlab "dev-digest-go/backend/internal/ci/adapters/gitlab"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
)

type cigitlabPublisher = cigitlab.Publisher

func newPublisher(s *stub, resolve glclient.ResolveFunc) *cigitlab.Publisher {
	return cigitlab.NewPublisher(s.client(), resolve)
}

// withBase points the binding a resolve func answers at base (the stub's address), keeping its
// verdict and error.
func withBase(resolve glclient.ResolveFunc, base string) glclient.ResolveFunc {
	return func(ctx context.Context, host string) (glclient.Binding, bool, error) {
		b, ok, err := resolve(ctx, host)
		if b.BaseURL != "" {
			b.BaseURL = base
		}
		return b, ok, err
	}
}

func newReader(s *stub, resolve glclient.ResolveFunc) *cigitlab.CI {
	return cigitlab.NewCI(s.client(), resolve, nil)
}
