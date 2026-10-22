// Package peers adapts the other contexts and the platform to the ports of the onboarding context
// (onboarding/app): the repo-intel index state and read port, and the clone's blobs at a commit.
// Each adapter only maps types; it holds no rule of its own. The peers are taken as small
// interfaces declared here, so the tests use fakes and the composition root passes the real
// *repointelapp.Service, *repointelapp.Reader and *gitcli.Client. The LLM client (*llm.Client) and
// the settings service (*settingsapp.Service) already satisfy the app's LLM and FeatureModels
// ports and are passed to onboarding/app.Deps as they are.
package peers
