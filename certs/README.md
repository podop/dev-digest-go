# certs/

Default `BUILD_CA_DIR` (see `.env.example`): every `*.crt` file here (PEM) is trusted by the image
builds — apk, `go mod download`, corepack/pnpm, `next build` font downloads — and added to the backend
runtime image's system trust store (git, GitHub, LLM providers). Needed behind a TLS-inspecting proxy
(Zscaler and the like). Empty = public roots only. Everything here except this README is git-ignored.

On a host that already trusts the proxy CA, point at its store instead of copying, e.g. in `.env`:
`BUILD_CA_DIR=/usr/local/share/ca-certificates`. Rebuild after a change: `make up`.
