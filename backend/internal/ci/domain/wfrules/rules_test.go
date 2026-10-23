package wfrules_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/ci/domain/wfrules"
)

const (
	checkoutPin = "actions/checkout@11d5960a326750d5838078e36cf38b85af677262"
	uploadPin   = "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02"
	guard       = "github.event.pull_request.head.repo.full_name == github.repository"
)

// generated is the workflow of spec 7.6.5 for the slug "test".
const generated = `name: DevDigest Review · test

on:
  pull_request:
    types: [opened, synchronize, reopened]

permissions:
  contents: read
  pull-requests: write

jobs:
  review:
    name: DevDigest Review · test
    if: github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-24.04
    steps:
      - name: Check out the pull request
        uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262
        with:
          ref: ${{ github.event.pull_request.head.sha }}
          persist-credentials: false

      - name: Run the DevDigest review
        shell: bash
        run: |
          base64 -d .devdigest/runner/devdigest-runner-linux-amd64.gz.b64 | gzip -dc > "$RUNNER_TEMP/devdigest-runner"
          cd "$RUNNER_TEMP" && sha256sum --check --strict "$GITHUB_WORKSPACE/.devdigest/runner/devdigest-runner-linux-amd64.sha256"
          chmod +x "$RUNNER_TEMP/devdigest-runner"
          cd "$GITHUB_WORKSPACE" && "$RUNNER_TEMP/devdigest-runner"
        env:
          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          DEVDIGEST_AGENT: test
          DEVDIGEST_POST_AS: github_review
          PR_HEAD_SHA: ${{ github.event.pull_request.head.sha }}

      - name: Upload the review result
        if: always()
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02
        with:
          name: devdigest-result-test
          path: devdigest-result.json
`

// nodeGenerated is the workflow the Node studio generates (with the Node setup step).
const nodeGenerated = `name: DevDigest Review · test

on:
  pull_request:
    types: [opened]

permissions:
  contents: read
  pull-requests: write

jobs:
  review:
    name: DevDigest Review · test
    if: github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-24.04
    steps:
      - name: Check out the pull request
        uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262
        with:
          ref: ${{ github.event.pull_request.head.sha }}
          persist-credentials: false

      - name: Set up Node.js
        uses: actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020
        with:
          node-version: '22'

      - name: Run the DevDigest review
        run: node .devdigest/runner/index.js
        env:
          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          DEVDIGEST_AGENT: test
          DEVDIGEST_POST_AS: none
          PR_HEAD_SHA: ${{ github.event.pull_request.head.sha }}
`

type wfCase struct {
	name string
	text string
	want wfrules.Rule // "" = passes
}

// edit replaces the first occurrence of old in generated; a missing old is a bug in the test.
func edit(t testing.TB, old, replacement string) string {
	t.Helper()
	if !strings.Contains(generated, old) {
		t.Fatalf("test bug: %q not in the generated workflow", old)
	}
	return strings.Replace(generated, old, replacement, 1)
}

// withTop returns the generated workflow with extra text appended at the top level.
func withTop(extra string) string { return generated + extra }

const (
	onBlock   = "on:\n  pull_request:\n    types: [opened, synchronize, reopened]\n"
	permBlock = "permissions:\n  contents: read\n  pull-requests: write\n"
	guardLine = "    if: " + guard + "\n"
)

func ruleCases(t testing.TB) []wfCase {
	t.Helper()
	cs := []wfCase{
		{"generated Go workflow", generated, ""},
		{"generated Node workflow", nodeGenerated, ""},
		{"CRLF line endings", strings.ReplaceAll(generated, "\n", "\r\n"), ""},
		{"extra top-level keys are ignored", withTop("env:\n  A: b\nconcurrency: x\n"), ""},

		// invalid_yaml
		{"empty text", "", wfrules.InvalidYAML},
		{"comment only", "# nothing\n", wfrules.InvalidYAML},
		{"document marker only", "---\n", wfrules.InvalidYAML},
		{"syntax error", "a: [b\n", wfrules.InvalidYAML},
		{"tab indentation", "jobs:\n\treview: {}\n", wfrules.InvalidYAML},
		{"unknown alias", withTop("x: *nope\n"), wfrules.InvalidYAML},
		{"root is a list", "- a\n- b\n", wfrules.InvalidYAML},
		{"root is a scalar", "hello\n", wfrules.InvalidYAML},
		{"jobs missing", onBlock + permBlock, wfrules.InvalidYAML},
		{"jobs empty", onBlock + permBlock + "jobs: {}\n", wfrules.InvalidYAML},
		{"jobs is a list", onBlock + permBlock + "jobs: [a]\n", wfrules.InvalidYAML},
		{"job is not a mapping", onBlock + permBlock + "jobs:\n  a: b\n", wfrules.InvalidYAML},
		{"second document", generated + "---\nsecond: true\n", wfrules.InvalidYAML},
		{"leading empty document then workflow", "---\n---\n" + generated, wfrules.InvalidYAML},
		{"trailing document marker", generated + "---\n", wfrules.InvalidYAML},
		{"duplicate top-level key", withTop("jobs:\n  other: {}\n"), wfrules.InvalidYAML},
		{"duplicate nested key", edit(t, "          persist-credentials: false\n", "          persist-credentials: false\n          ref: x\n"), wfrules.InvalidYAML},
		{"duplicate key in flow mapping", withTop("m: {a: 1, a: 2}\n"), wfrules.InvalidYAML},
		{"duplicate key spelled with quotes", withTop("'q': 1\n\"q\": 2\n"), wfrules.InvalidYAML},
		{"duplicate numeric keys 1 and 1.0", withTop("1: a\n1.0: b\n"), wfrules.InvalidYAML},
		{"duplicate merge keys", withTop("<<: {a: 1}\n<<: {b: 2}\n"), wfrules.InvalidYAML},
		{"duplicate key hiding a second uses", edit(t, "        uses: "+checkoutPin+"\n", "        uses: "+checkoutPin+"\n        uses: evil/x@main\n"), wfrules.InvalidYAML},
		{"string and number keys are distinct", withTop("1: a\n\"1\": b\n"), ""},
		{"duplicate null keys", withTop("? \n: a\n? ~\n: b\n"), wfrules.InvalidYAML},
		{"NaN keys never collide", withTop(".nan: a\n.NaN: b\n"), ""},
		{"set tag on a mapping (deviation: Node accepts)", withTop("s: !!set {a, b}\n"), wfrules.InvalidYAML},
		{"omap tag hiding steps from the rules (deviation: Node accepts)", strings.Replace(generated, "    steps:\n", "    steps: !!omap\n", 1), wfrules.InvalidYAML},
		{"local tag on a sequence (deviation: Node accepts)", withTop("s: !local [a]\n"), wfrules.InvalidYAML},
		{"map tag on a sequence (deviation: Node accepts)", withTop("s: !!map [a]\n"), wfrules.InvalidYAML},
		{"plain tags on collections", withTop("s: !!seq [a]\nm: !!map {b: c}\n"), ""},
		{"unknown scalar tag is a string", withTop("s: !local value\n"), ""},
		{"circular alias (deviation: Node overflows its stack)", withTop("loop: &l [*l]\n"), wfrules.InvalidYAML},
		{"circular alias through a mapping (deviation: Node overflows its stack)", withTop("loop: &l {a: *l}\n"), wfrules.InvalidYAML},
		{"alias to an earlier anchor", withTop("a: &x [1, 2]\nb: *x\n"), ""},
		{"alias key and anchored key", withTop("&k kname: 1\n*k : 2\n"), ""},
		{"size limit is exclusive", paddedTo(generated, 65_536), ""},
		{"over the size limit", paddedTo(generated, 65_537), wfrules.InvalidYAML},
		{"over the size limit, multi-byte", paddedTo(generated, 65_535) + "é", wfrules.InvalidYAML},

		// alias budget (Node: uses of an anchor + 1, times the nesting factor, may not exceed 100)
		{"99 aliases of one scalar", withTop(aliasList(99)), ""},
		{"100 aliases of one scalar", withTop(aliasList(100)), wfrules.InvalidYAML},
		{"billion laughs", withTop(billionLaughs(9, 9)), wfrules.InvalidYAML},
		{"billion laughs with a short chain", withTop(billionLaughs(3, 3)), ""},
		{"alias fan-out over empty collections", withTop(emptyLaughs(10, 10)), wfrules.InvalidYAML},
		{"deep flow sequence", withTop("deep: " + strings.Repeat("[", 20_000) + strings.Repeat("]", 20_000) + "\n"), wfrules.InvalidYAML},
		{"flow sequence at the depth limit", withTop("deep: " + strings.Repeat("[", 600) + strings.Repeat("]", 600) + "\n"), ""},
		{"deep block mappings", withTop(deepBlock(5_000)), wfrules.InvalidYAML},
		{"one 60 KB scalar", withTop("big: " + strings.Repeat("a", 60_000) + "\n"), ""},
		{"60 KB of unterminated expression openers", withTop("big: \"" + strings.Repeat("${{", 20_000) + "\"\n"), ""},
		{"20 000 tiny expressions", withTop("big: \"" + strings.Repeat("${{a}}", 9_000) + "\"\n"), ""},
		{"many small anchors", withTop(manyAnchors(3_000)), ""},

		// forbidden_trigger
		{"on is a string", edit(t, onBlock, "on: pull_request\n"), wfrules.ForbiddenTrigger},
		{"on is a list", edit(t, onBlock, "on: [pull_request]\n"), wfrules.ForbiddenTrigger},
		{"on missing", edit(t, onBlock, ""), wfrules.ForbiddenTrigger},
		{"pull_request_target", edit(t, onBlock, "on:\n  pull_request_target:\n"), wfrules.ForbiddenTrigger},
		{"push", edit(t, onBlock, "on:\n  push:\n"), wfrules.ForbiddenTrigger},
		{"two triggers", edit(t, onBlock, "on:\n  pull_request:\n  push:\n"), wfrules.ForbiddenTrigger},
		{"workflow_dispatch added", edit(t, onBlock, "on:\n  pull_request:\n    types: [opened]\n  workflow_dispatch:\n"), wfrules.ForbiddenTrigger},
		{"pull_request is a string", edit(t, onBlock, "on:\n  pull_request: opened\n"), wfrules.ForbiddenTrigger},
		{"pull_request is a list", edit(t, onBlock, "on:\n  pull_request: [opened]\n"), wfrules.ForbiddenTrigger},
		{"unknown type", edit(t, onBlock, "on:\n  pull_request:\n    types: [opened, labeled]\n"), wfrules.ForbiddenTrigger},
		{"types holds a non-string", edit(t, onBlock, "on:\n  pull_request:\n    types: [opened, 1]\n"), wfrules.ForbiddenTrigger},
		{"types is null", edit(t, onBlock, "on:\n  pull_request:\n    types:\n"), wfrules.ForbiddenTrigger},
		{"types is a map", edit(t, onBlock, "on:\n  pull_request:\n    types: {opened: 1}\n"), wfrules.ForbiddenTrigger},
		{"pull_request null", edit(t, onBlock, "on:\n  pull_request:\n"), ""},
		{"pull_request empty flow mapping", edit(t, onBlock, "on:\n  pull_request: {}\n"), ""},
		{"types as a string", edit(t, onBlock, "on:\n  pull_request:\n    types: reopened\n"), ""},
		{"empty types list", edit(t, onBlock, "on:\n  pull_request:\n    types: []\n"), ""},
		{"branch filter is allowed", edit(t, onBlock, "on:\n  pull_request:\n    branches: [main]\n"), ""},
		{"quoted on key", edit(t, onBlock, "\"on\":\n  pull_request:\n"), ""},
		{"YAML 1.2: on is a plain string key", edit(t, onBlock, "on:\n  pull_request:\n"), ""},
		{"YAML 1.2: true key is not on", edit(t, onBlock, "true:\n  pull_request:\n"), wfrules.ForbiddenTrigger},
		{"YAML 1.2: types yes is a string and not allowed", edit(t, onBlock, "on:\n  pull_request:\n    types: [yes]\n"), wfrules.ForbiddenTrigger},

		// permissions
		{"permissions missing", edit(t, permBlock, ""), wfrules.Permissions},
		{"permissions write-all", edit(t, permBlock, "permissions: write-all\n"), wfrules.Permissions},
		{"permissions read-all", edit(t, permBlock, "permissions: read-all\n"), wfrules.Permissions},
		{"permissions empty", edit(t, permBlock, "permissions: {}\n"), wfrules.Permissions},
		{"permissions null", edit(t, permBlock, "permissions:\n"), wfrules.Permissions},
		{"contents write", edit(t, "contents: read", "contents: write"), wfrules.Permissions},
		{"pull-requests read", edit(t, "pull-requests: write", "pull-requests: read"), wfrules.Permissions},
		{"extra permission", edit(t, permBlock, permBlock+"  id-token: write\n"), wfrules.Permissions},
		{"missing permission", edit(t, permBlock, "permissions:\n  contents: read\n"), wfrules.Permissions},
		{"permission name case", edit(t, "contents: read", "Contents: read"), wfrules.Permissions},
		{"merge key counts as a permissions key", edit(t, permBlock, "permissions:\n  <<: {a: b}\n  contents: read\n  pull-requests: write\n"), wfrules.Permissions},
		{"job grants contents write", edit(t, guardLine, guardLine+"    permissions:\n      contents: write\n"), wfrules.Permissions},
		{"job grants write-all", edit(t, guardLine, guardLine+"    permissions: write-all\n"), wfrules.Permissions},
		{"job permissions null", edit(t, guardLine, guardLine+"    permissions:\n"), wfrules.Permissions},
		{"job permissions list", edit(t, guardLine, guardLine+"    permissions: [contents]\n"), wfrules.Permissions},
		{"job grants pull-requests write", edit(t, guardLine, guardLine+"    permissions:\n      pull-requests: write\n"), ""},
		{"job reads everything", edit(t, guardLine, guardLine+"    permissions:\n      contents: read\n      issues: read\n"), ""},
		{"job permissions empty mapping", edit(t, guardLine, guardLine+"    permissions: {}\n"), ""},
		{"second job grants write", withJob("  other:\n    if: " + guard + "\n    permissions:\n      issues: write\n"), wfrules.Permissions},

		// secrets
		{"job secrets inherit", edit(t, guardLine, guardLine+"    secrets: inherit\n"), wfrules.Secrets},
		{"job secrets mapping", edit(t, guardLine, guardLine+"    secrets:\n      A: b\n"), wfrules.Secrets},
		{"other secret", edit(t, "${{ secrets.GITHUB_TOKEN }}", "${{ secrets.NPM_TOKEN }}"), wfrules.Secrets},
		{"secrets lower-case name", edit(t, "secrets.GITHUB_TOKEN", "secrets.github_token"), wfrules.Secrets},
		{"bare secrets object", edit(t, "${{ secrets.GITHUB_TOKEN }}", "${{ toJSON(secrets) }}"), wfrules.Secrets},
		{"secrets by index", edit(t, "secrets.GITHUB_TOKEN", "secrets['GITHUB_TOKEN']"), wfrules.Secrets},
		{"secrets upper-case keyword and allowed name", edit(t, "secrets.GITHUB_TOKEN", "SECRETS.GITHUB_TOKEN"), ""},
		{"secrets upper-case keyword and other name", edit(t, "secrets.GITHUB_TOKEN", "Secrets.OTHER"), wfrules.Secrets},
		{"secrets word inside another word", edit(t, "${{ secrets.GITHUB_TOKEN }}", "${{ mysecrets.X }}"), ""},
		{"secret in a second expression of one scalar", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github.sha }} ${{ secrets.OTHER }}"), wfrules.Secrets},
		{"secrets reference in an if value", edit(t, "        if: always()", "        if: secrets.OTHER != ''"), wfrules.Secrets},
		{"secrets outside an expression is not a reference", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: secrets.OTHER"), ""},
		{"RE2 case folding: long s is not s", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ \u017fecrets.OTHER }}"), ""},

		// unpinned_action
		{"action by tag", edit(t, "uses: "+checkoutPin, "uses: actions/checkout@v4"), wfrules.UnpinnedAction},
		{"action by branch", edit(t, "uses: "+checkoutPin, "uses: actions/checkout@main"), wfrules.UnpinnedAction},
		{"action with a short sha", edit(t, "uses: "+checkoutPin, "uses: actions/checkout@11d5960"), wfrules.UnpinnedAction},
		{"action with a 39-char sha", edit(t, checkoutPin, "actions/checkout@11d5960a326750d5838078e36cf38b85af67726"), wfrules.UnpinnedAction},
		{"action with a 41-char sha", edit(t, checkoutPin, checkoutPin+"2"), wfrules.UnpinnedAction},
		{"action with a non-hex sha", edit(t, checkoutPin, "actions/checkout@g1d5960a326750d5838078e36cf38b85af677262"), wfrules.UnpinnedAction},
		{"action without a ref", edit(t, "uses: "+checkoutPin, "uses: actions/checkout"), wfrules.UnpinnedAction},
		{"local action", edit(t, "uses: "+checkoutPin, "uses: ./local@11d5960a326750d5838078e36cf38b85af677262"), wfrules.UnpinnedAction},
		{"parent-directory action", edit(t, "uses: "+checkoutPin, "uses: ../x/y@11d5960a326750d5838078e36cf38b85af677262"), wfrules.UnpinnedAction},
		{"dot repository", edit(t, "uses: "+checkoutPin, "uses: a/.@11d5960a326750d5838078e36cf38b85af677262"), wfrules.UnpinnedAction},
		{"dot-dot repository", edit(t, "uses: "+checkoutPin, "uses: a/..@11d5960a326750d5838078e36cf38b85af677262"), wfrules.UnpinnedAction},
		{"dot-dot repository with path", edit(t, "uses: "+checkoutPin, "uses: a/../b@11d5960a326750d5838078e36cf38b85af677262"), wfrules.UnpinnedAction},
		{"docker image", edit(t, "uses: "+checkoutPin, "uses: docker://alpine@sha256:abc"), wfrules.UnpinnedAction},
		{"uses is a number", edit(t, "uses: "+checkoutPin, "uses: 12"), wfrules.UnpinnedAction},
		{"uses is null", edit(t, "uses: "+checkoutPin, "uses:"), wfrules.UnpinnedAction},
		{"uses with a trailing newline", edit(t, "uses: "+checkoutPin, "uses: \""+checkoutPin+"\\n\""), wfrules.UnpinnedAction},
		{"uses with a leading space inside quotes", edit(t, "uses: "+checkoutPin, "uses: \" "+checkoutPin+"\""), wfrules.UnpinnedAction},
		{"unpinned second step", edit(t, "uses: "+uploadPin, "uses: actions/upload-artifact@v4"), wfrules.UnpinnedAction},
		{"job-level uses unpinned", edit(t, guardLine, guardLine+"    uses: org/repo/.github/workflows/w.yml@main\n"), wfrules.UnpinnedAction},
		{"job-level uses pinned with a path", edit(t, guardLine, guardLine+"    uses: org/repo/.github/workflows/w.yml@11d5960a326750d5838078e36cf38b85af677262\n"), ""},
		{"upper-case hex sha", edit(t, checkoutPin, "actions/checkout@11D5960A326750D5838078E36CF38B85AF677262"), ""},
		{"path with dots after the repository", edit(t, "uses: "+checkoutPin, "uses: a/b/.dir/../c@11d5960a326750d5838078e36cf38b85af677262"), ""},
		{"steps is not a list", strings.Replace(generated, "    steps:\n", "    steps: x\n    xsteps:\n", 1), ""},
		{"a step that is not a mapping is skipped", edit(t, "    steps:\n", "    steps:\n      - just a string\n"), ""},

		// fork_guard
		{"job without if", edit(t, guardLine, ""), wfrules.ForkGuard},
		{"if is true", edit(t, guardLine, "    if: true\n"), wfrules.ForkGuard},
		{"if is always()", edit(t, guardLine, "    if: always()\n"), wfrules.ForkGuard},
		{"if weakened with or", edit(t, guardLine, "    if: "+guard+" || true\n"), wfrules.ForkGuard},
		{"if compares the wrong repo", edit(t, guardLine, "    if: github.event.pull_request.head.repo.full_name == github.event.repository.full_name\n"), wfrules.ForkGuard},
		{"if negated", edit(t, guardLine, "    if: github.event.pull_request.head.repo.full_name != github.repository\n"), wfrules.ForkGuard},
		{"if in other case", edit(t, guardLine, "    if: GITHUB.event.pull_request.head.repo.full_name == github.repository\n"), wfrules.ForkGuard},
		{"if wrapped in an expression", edit(t, guardLine, "    if: ${{ "+guard+" }}\n"), ""},
		{"if wrapped without spaces", edit(t, guardLine, "    if: ${{"+guard+"}}\n"), ""},
		{"if with collapsed whitespace", edit(t, guardLine, "    if: github.event.pull_request.head.repo.full_name   ==\ttrue_placeholder\n"), wfrules.ForkGuard},
		{"if with extra inner spaces", edit(t, guardLine, "    if: \"  github.event.pull_request.head.repo.full_name  ==  github.repository  \"\n"), ""},
		{"if folded over lines", edit(t, guardLine, "    if: >-\n      github.event.pull_request.head.repo.full_name\n      == github.repository\n"), ""},
		{"if with a no-break space", edit(t, guardLine, "    if: \"github.event.pull_request.head.repo.full_name\u00a0==\u00a0github.repository\"\n"), ""},
		{"second job without a guard", withJob("  other:\n    runs-on: x\n"), wfrules.ForkGuard},
		{"second job with the guard", withJob("  other:\n    if: " + guard + "\n"), ""},
		{"if is a list", edit(t, guardLine, "    if: [a]\n"), wfrules.ForkGuard},
		{"if with a trailing comment", edit(t, guardLine, "    if: "+guard+" # fine\n"), ""},

		// untrusted_expression
		{"pull request title", edit(t, "${{ github.event.pull_request.head.sha }}\n          persist", "${{ github.event.pull_request.title }}\n          persist"), wfrules.UntrustedExpression},
		{"pull request body in run", edit(t, "          chmod +x", "          echo \"${{ github.event.pull_request.body }}\"\n          chmod +x"), wfrules.UntrustedExpression},
		{"head ref", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github.event.pull_request.head.ref }}"), wfrules.UntrustedExpression},
		{"head label", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github.event.pull_request.head.label }}"), wfrules.UntrustedExpression},
		{"head_ref", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github.head_ref }}"), wfrules.UntrustedExpression},
		{"comment body", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github.event.comment.body }}"), wfrules.UntrustedExpression},
		{"toJSON(github)", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ toJSON(github) }}"), wfrules.UntrustedExpression},
		{"toJSON(github.event)", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ toJSON(github.event) }}"), wfrules.UntrustedExpression},
		{"toJSON(github.event.pull_request)", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ toJSON(github.event.pull_request) }}"), wfrules.UntrustedExpression},
		{"index notation", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github['event']['pull_request']['title'] }}"), wfrules.UntrustedExpression},
		{"index notation, double quotes, spaces", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github.event.pull_request[ \"body\" ] }}"), wfrules.UntrustedExpression},
		{"index notation, bare", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github.event.pull_request[title] }}"), wfrules.UntrustedExpression},
		{"upper-case", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ GitHub.Event.Pull_Request.Title }}"), wfrules.UntrustedExpression},
		{"spaces around dots", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github . event . pull_request . title }}"), wfrules.UntrustedExpression},
		{"newline inside the expression", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: \"${{ github.event.\\npull_request.title }}\""), wfrules.UntrustedExpression},
		{"no-break space inside the expression", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: \"${{ github.event.pull_request.\u00a0title }}\""), wfrules.UntrustedExpression},
		{"BOM inside the expression", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: \"${{ github.event.pull_request.\ufefftitle }}\""), wfrules.UntrustedExpression},
		{"expression in a step if", edit(t, "        if: always()", "        if: contains(github.head_ref, 'x')"), wfrules.UntrustedExpression},
		{"wrapped expression in a step if", edit(t, "        if: always()", "        if: ${{ github.head_ref == 'x' }}"), wfrules.UntrustedExpression},
		{"expression in a name", edit(t, "name: Run the DevDigest review", "name: Run ${{ github.event.pull_request.title }}"), wfrules.UntrustedExpression},
		{"expression in a block scalar", edit(t, "          chmod +x", "          echo ${{ github.event.comment.body }}\n          chmod +x"), wfrules.UntrustedExpression},
		{"expression in a list element", withTop("x:\n  - ${{ github.head_ref }}\n"), wfrules.UntrustedExpression},
		{"expression in a nested flow mapping", withTop("x: {a: [{b: \"${{ github.head_ref }}\"}]}\n"), wfrules.UntrustedExpression},
		{"expression in a quoted scalar", withTop("x: '${{ github.head_ref }}'\n"), wfrules.UntrustedExpression},
		{"second expression of one scalar", withTop("x: \"${{ github.sha }} ${{ github.head_ref }}\"\n"), wfrules.UntrustedExpression},
		{"untrusted text outside an expression", withTop("x: github.head_ref\n"), ""},
		{"untrusted text in a key is not inspected", withTop("\"${{ github.head_ref }}\": 1\n"), ""},
		{"untrusted text in a number-looking scalar", withTop("x: 12\n"), ""},
		{"head sha is trusted", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github.event.pull_request.head.sha }}"), ""},
		{"github.sha is trusted", edit(t, "DEVDIGEST_AGENT: test", "DEVDIGEST_AGENT: ${{ github.sha }}"), ""},
		{"RE2 case folding: long s is not s", withTop("x: \"${{ github.event.pull_request.title }}\"\ny: \"${{ github.event.pull_request.titl\u017f }}\"\n"), wfrules.UntrustedExpression},
		{"capital I with dot lower-cases to two code points", withTop("x: \"${{ G\u0130thub.head_ref }}\"\n"), ""},
		{"kelvin sign is not k in secrets", withTop("x: \"${{ secrets.\u212a }}\"\n"), wfrules.Secrets},
		{"unterminated expression", withTop("x: \"${{ github.head_ref\"\n"), ""},

		// merge keys are plain keys
		{"merge key is a plain key (no uses on the job)", edit(t, guardLine, guardLine+"    <<: {uses: evil/x@main}\n"), ""},
		{"merge key is a plain key (no secrets on the job)", edit(t, guardLine, guardLine+"    <<: {secrets: inherit}\n"), ""},
		{"merge key with an alias", withTop("base: &b {a: 1}\nmerged:\n  <<: *b\n  c: 2\n"), ""},
		{"merge key content is still inspected for expressions", withTop("m:\n  <<: {x: \"${{ github.head_ref }}\"}\n"), wfrules.UntrustedExpression},

		// scalars and tags
		{"explicit string tag", withTop("s: !!str 12\n"), ""},
		{"explicit int tag on text", withTop("s: !!int abc\n"), ""},
		{"binary tag", withTop("s: !!binary aGVsbG8=\n"), ""},
		{"anchored scalar used as a value of another key", withTop("&n nval: v\nother: *n\n"), ""},
		{"first rule wins: trigger before permissions", edit(t, onBlock+"\n"+permBlock, "on: push\n"), wfrules.ForbiddenTrigger},
		{"first rule wins: permissions before secrets", strings.Replace(edit(t, "contents: read", "contents: write"), "secrets.GITHUB_TOKEN", "secrets.OTHER", 1), wfrules.Permissions},
		{"first rule wins: secrets before pins", strings.Replace(edit(t, "uses: "+checkoutPin, "uses: a/b@main"), "secrets.GITHUB_TOKEN", "secrets.OTHER", 1), wfrules.Secrets},
		{"first rule wins: pins before guard", edit(t, guardLine, "    if: true\n    uses: a/b@main\n"), wfrules.UnpinnedAction},
		{"first rule wins: guard before expressions", strings.Replace(edit(t, guardLine, ""), "${{ github.event.pull_request.head.sha }}", "${{ github.head_ref }}", 1), wfrules.ForkGuard},
	}
	return cs
}

func TestCheck(t *testing.T) {
	for _, tc := range ruleCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			got := wfrules.Check(tc.text)
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("Check(%s) took %v, want a bounded run", tc.name, elapsed)
			}
			want := wfrules.Result{OK: tc.want == "", Rule: tc.want}
			if got != want {
				t.Errorf("Check(%s) = %+v, want %+v", tc.name, got, want)
			}
		})
	}
}

// The limits wfrules repeats must stay equal to the domain constants.
func TestConstantsMatchTheDomain(t *testing.T) {
	big := paddedTo(generated, domain.MaxWorkflowBytes)
	if got := wfrules.Check(big); !got.OK {
		t.Errorf("Check(workflow of exactly domain.MaxWorkflowBytes = %d bytes) = %+v, want OK", domain.MaxWorkflowBytes, got)
	}
	if got := wfrules.Check(big + "#"); got.Rule != wfrules.InvalidYAML {
		t.Errorf("Check(workflow of MaxWorkflowBytes+1 bytes) = %+v, want invalid_yaml", got)
	}
	for _, trig := range domain.Triggers() {
		text := strings.Replace(generated, "types: [opened, synchronize, reopened]", "types: ["+string(trig)+"]", 1)
		if got := wfrules.Check(text); !got.OK {
			t.Errorf("Check(types: [%s]) = %+v, want OK", trig, got)
		}
	}
}

func TestCheckIsRepeatable(t *testing.T) {
	for range 3 {
		if got := wfrules.Check(generated); !got.OK {
			t.Fatalf("Check(generated) = %+v, want OK", got)
		}
	}
}

func FuzzCheck(f *testing.F) {
	f.Add(generated)
	f.Add(nodeGenerated)
	f.Add("a: &a [*a]")
	f.Add("--- a\n--- b\n")
	f.Add("on: {pull_request: {types: !!set {opened}}}")
	f.Fuzz(func(t *testing.T, text string) {
		start := time.Now()
		got := wfrules.Check(text)
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("Check(%q) took %v, want a bounded run", text, elapsed)
		}
		if got.OK && got.Rule != "" {
			t.Errorf("Check(%q) = %+v, want OK with no rule", text, got)
		}
		if !got.OK && got.Rule == "" {
			t.Errorf("Check(%q) = %+v, want a rule when not OK", text, got)
		}
	})
}

// withJob appends a second job to the generated workflow (jobs is its last top-level key).
func withJob(job string) string { return generated + job }

// paddedTo appends a YAML comment so that the text is exactly n bytes long.
func paddedTo(text string, n int) string {
	pad := n - len(text) - len("#\n")
	if pad < 0 {
		panic("paddedTo: text longer than " + strconv.Itoa(n))
	}
	return text + "#" + strings.Repeat("x", pad) + "\n"
}

func aliasList(n int) string {
	return "anchor: &a hello\nuses_a: [" + strings.TrimSuffix(strings.Repeat("*a, ", n), ", ") + "]\n"
}

// billionLaughs builds `levels` anchored lists of `width` aliases of the previous one.
func billionLaughs(levels, width int) string {
	var b strings.Builder
	b.WriteString("l0: &l0 [lol, lol, lol, lol, lol, lol, lol, lol, lol]\n")
	for i := 1; i <= levels; i++ {
		prev := "*l" + strconv.Itoa(i-1)
		b.WriteString("l" + strconv.Itoa(i) + ": &l" + strconv.Itoa(i) + " [" + strings.TrimSuffix(strings.Repeat(prev+", ", width), ", ") + "]\n")
	}
	return b.String()
}

// emptyLaughs is billionLaughs over empty lists: the alias metric stays 0, only the visit budget stops it.
func emptyLaughs(levels, width int) string {
	var b strings.Builder
	b.WriteString("e0: &e0 []\n")
	for i := 1; i <= levels; i++ {
		prev := "*e" + strconv.Itoa(i-1)
		b.WriteString("e" + strconv.Itoa(i) + ": &e" + strconv.Itoa(i) + " [" + strings.TrimSuffix(strings.Repeat(prev+", ", width), ", ") + "]\n")
	}
	return b.String()
}

func deepBlock(depth int) string {
	var b strings.Builder
	b.WriteString("deep:\n")
	for i := 1; i <= depth; i++ {
		b.WriteString(strings.Repeat(" ", i) + "k:\n")
	}
	return b.String()
}

func manyAnchors(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString("a" + strconv.Itoa(i) + ": &x" + strconv.Itoa(i) + " v\n")
	}
	return b.String()
}
