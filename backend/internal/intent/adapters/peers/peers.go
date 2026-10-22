// Package peers adapts the platform clients to the ports of the intent context (intent/app): the
// doc reader over the clone and the GitHub contents API (spec 7.4.3, 7.13) and the ticket reader
// over the GitHub issues API. Each adapter turns every failure into data (a failed source with a
// reason) and holds no rule of the derivation itself. The clients are taken as small interfaces
// declared here, so the tests use real ones against fixtures and the composition root passes
// *gitcli.Client and *github.Client.
package peers
