// Package peers adapts the other contexts and the platform to the ports of the reviews context
// (reviews/app): the run bus, the LLM providers, the repo-intel read port and the project-context
// resolver. Each adapter only maps types; it holds no rule of its own. The peers are taken as
// small interfaces declared here, so the tests use fakes and the composition root passes the real
// *runbus.Bus, *llm.Client, *repointelapp.Reader and *projectcontextapp.Service.
package peers
