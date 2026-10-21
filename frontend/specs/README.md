# specs

Feature specs for this package — one file per feature. New specs:
`<YYYY-MM-DD>-<feature-slug>.md`; older ones keep their `NN-feature-name.md` names.
A feature that touches several modules has one spec in the root [`specs/`](../../specs/README.md).

Written by the `specreator` agent (`claude --agent specreator`); template and rules in
[`.claude/agents/specreator.md`](../../.claude/agents/specreator.md). Keep it about
*what* and *why* (scenarios, requirements, contracts, acceptance criteria); the code is
the *how*.
