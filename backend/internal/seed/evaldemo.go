package seed

import (
	"context"
	"fmt"
	"strings"
)

// The PR #483 eval demo: a second pull request of acme/payments-api, reviewed by the Security
// Reviewer, with accepted and dismissed findings so "Turn into eval case" has both kinds of
// decided findings to work with. Ported from the dev-digest Node seed (server/src/db/
// seed-eval-demo.ts, PR #483; spec D6021, FR6057); the text is verbatim, the hunk text is
// compared with Node's output in testdata/pr483_patches.golden.json.
const (
	evalDemoPRNumber    = 483
	evalDemoAgentName   = "Security Reviewer"
	evalDemoModel       = "seed-eval-demo"
	evalDemoCommitSHA   = "c3d4e5f6a7b8"
	evalDemoScore       = 34
	evalDemoDurationMs  = 21_350
	evalDemoTokensIn    = 5_870
	evalDemoTokensOut   = 1_460
	evalDemoCostUSD     = 0.002412
	evalDemoPRTitle     = "Add admin data export and Stripe webhook verification"
	evalDemoPRAuthor    = "dev.rivera"
	evalDemoPRBranch    = "feat/admin-export"
	evalDemoSummary     = "Several exploitable issues: injection and missing auth on the admin export, and unverified JWT handling. A few lower-confidence notes are style-level."
	evalDemoCommitMsg   = "Add admin export, Stripe webhook handler and session tokens"
	evalDemoPRBody      = "Adds an admin CSV/JSON export, verifies Stripe webhook signatures and issues session tokens for the admin app."
	fateAccepted        = "accepted"
	fateDismissed       = "dismissed"
	fatePending         = "pending"
	evalDemoExportPath  = "src/api/admin/export.ts"
	evalDemoStripePath  = "src/webhooks/stripe.ts"
	evalDemoSessionPath = "src/auth/session.ts"
	evalDemoInvoicePath = "src/billing/invoices.ts"
)

// evalDemoFile is one new file of PR #483; every line is an added line.
type evalDemoFile struct {
	path  string
	lines []string
}

// evalDemoFiles are the four new files, in Node's insertion order.
var evalDemoFiles = []evalDemoFile{
	{evalDemoExportPath, []string{
		"import type { FastifyInstance } from 'fastify';",
		"import { exec } from 'node:child_process';",
		"import { sql } from 'drizzle-orm';",
		"import { db } from '../../db';",
		"",
		"export async function adminExportRoutes(app: FastifyInstance) {",
		"  app.get('/admin/export', async (req, reply) => {",
		"    const { table, format, filter } = req.query as Record<string, string>;",
		"    const rows = await db.execute(sql.raw(`SELECT * FROM ${table} WHERE ${filter ?? '1=1'}`));",
		"    const file = `/tmp/export-${table}.${format}`;",
		"    exec(`node scripts/dump.js ${file} --format ${format}`, (err) => {",
		"      if (err) console.error(err);",
		"    });",
		"    reply.header('Access-Control-Allow-Origin', '*');",
		"    return { rows, file };",
		"  });",
		"",
		"  app.get('/admin/export/download', async (req, reply) => {",
		"    const { name } = req.query as { name: string };",
		"    return reply.sendFile(`../exports/${name}`);",
		"  });",
		"}",
	}},
	{evalDemoStripePath, []string{
		"import crypto from 'node:crypto';",
		"import type { FastifyRequest, FastifyReply } from 'fastify';",
		"import { config } from '../config';",
		"import { markInvoicePaid } from '../billing/invoices';",
		"",
		"export async function handleStripeEvent(req: FastifyRequest, reply: FastifyReply) {",
		"  const signature = req.headers['stripe-signature'] as string;",
		"  const payload = JSON.stringify(req.body);",
		"  const expected = crypto.createHmac('sha256', config.webhookSecret).update(payload).digest('hex');",
		"  if (signature === expected) {",
		"    await markInvoicePaid(req.body.data.object.id);",
		"  }",
		"  return reply.code(200).send({ received: true });",
		"}",
	}},
	{evalDemoSessionPath, []string{
		"import jwt from 'jsonwebtoken';",
		"import { config } from '../config';",
		"",
		"export function createSession(userId: string, role: string) {",
		"  return jwt.sign({ sub: userId, role }, config.jwtSecret ?? 'dev-secret', { expiresIn: '30d' });",
		"}",
		"",
		"export function readSession(token: string) {",
		"  return jwt.decode(token) as { sub: string; role: string } | null;",
		"}",
	}},
	{evalDemoInvoicePath, []string{
		"import { eq } from 'drizzle-orm';",
		"import { db } from '../db';",
		"import { invoices } from '../db/schema';",
		"",
		"export async function markInvoicePaid(id: string) {",
		"  console.log(`marking invoice ${id} paid`);",
		"  const receipt = `rcpt_${Math.random().toString(36).slice(2)}`;",
		"  await db.update(invoices).set({ status: 'paid', receipt }).where(eq(invoices.id, id));",
		"}",
	}},
}

// evalDemoPatches returns the new-file hunk of every file, keyed by path: a "@@ -0,0 +1,N @@"
// header, then every line prefixed with "+", joined with "\n" and no trailing newline.
func evalDemoPatches() map[string]string {
	out := make(map[string]string, len(evalDemoFiles))
	for _, f := range evalDemoFiles {
		hunk := make([]string, 0, len(f.lines)+1)
		hunk = append(hunk, fmt.Sprintf("@@ -0,0 +1,%d @@", len(f.lines)))
		for _, l := range f.lines {
			hunk = append(hunk, "+"+l)
		}
		out[f.path] = strings.Join(hunk, "\n")
	}
	return out
}

// evalDemoFinding is one finding before its line is resolved: at is a substring of the added
// line it points at (the first line containing it wins).
type evalDemoFinding struct {
	file       string
	at         string
	severity   string
	category   string
	title      string
	rationale  string
	suggestion string
	confidence float64
	fate       string
}

var evalDemoFindings = []evalDemoFinding{
	{
		file: evalDemoExportPath, at: "sql.raw(", severity: "CRITICAL", category: "security",
		title:      "SQL injection through table and filter query parameters",
		rationale:  "`table` and `filter` come straight from the query string and are interpolated into a raw SQL string, so any caller can read or modify arbitrary tables.",
		suggestion: "Allow-list the table name and pass the filter as bound parameters instead of `sql.raw`.",
		confidence: 0.97, fate: fateAccepted,
	},
	{
		file: evalDemoExportPath, at: "exec(`node scripts/dump.js", severity: "CRITICAL", category: "security",
		title:      "Command injection in export shell command",
		rationale:  "`file` and `format` are user-controlled and concatenated into a shell command passed to `exec`, which allows arbitrary command execution on the API host.",
		suggestion: "Use `execFile` with an argument array and validate `format` against a fixed list.",
		confidence: 0.96, fate: fateAccepted,
	},
	{
		file: evalDemoExportPath, at: "sendFile(`../exports/", severity: "CRITICAL", category: "security",
		title:      "Path traversal in export download",
		rationale:  "`name` is joined into a relative path without normalisation, so `../../etc/passwd` escapes the exports directory.",
		suggestion: "Resolve the path and reject anything outside the exports directory, or serve by an opaque id.",
		confidence: 0.94, fate: fateAccepted,
	},
	{
		file: evalDemoExportPath, at: "app.get('/admin/export', async", severity: "CRITICAL", category: "security",
		title:      "Admin export endpoints have no authentication",
		rationale:  "Neither /admin/export route registers an auth or role check, so any unauthenticated client can dump data.",
		suggestion: "Register the routes behind the admin auth hook and require the admin role.",
		confidence: 0.91, fate: fateAccepted,
	},
	{
		file: evalDemoExportPath, at: "Access-Control-Allow-Origin", severity: "WARNING", category: "security",
		title:      "Wildcard CORS on an admin endpoint",
		rationale:  "Allowing every origin on a data-export route lets any web page read the response from a logged-in admin browser.",
		suggestion: "Drop the header or restrict it to the admin app origin.",
		confidence: 0.82, fate: fateAccepted,
	},
	{
		file: evalDemoStripePath, at: "signature === expected", severity: "WARNING", category: "security",
		title:      "Webhook signature compared with a non-constant-time check",
		rationale:  "A plain string comparison of the HMAC leaks timing information that can help forge a valid signature.",
		suggestion: "Compare with `crypto.timingSafeEqual` on equal-length buffers.",
		confidence: 0.85, fate: fateAccepted,
	},
	{
		file: evalDemoSessionPath, at: "?? 'dev-secret'", severity: "CRITICAL", category: "security",
		title:      "JWT signed with a hardcoded fallback secret",
		rationale:  "When `jwtSecret` is unset the token is signed with a guessable constant, so anyone can mint valid sessions.",
		suggestion: "Fail fast at startup when the secret is missing; never fall back to a literal.",
		confidence: 0.95, fate: fateAccepted,
	},
	{
		file: evalDemoSessionPath, at: "jwt.decode(token)", severity: "CRITICAL", category: "security",
		title:      "Session token read with decode instead of verify",
		rationale:  "`jwt.decode` does not check the signature or expiry, so a forged token is accepted as a valid session.",
		suggestion: "Use `jwt.verify` with the server secret and the expected algorithm.",
		confidence: 0.96, fate: fateAccepted,
	},
	{
		file: evalDemoStripePath, at: "markInvoicePaid(req.body", severity: "WARNING", category: "security",
		title:      "No replay protection on Stripe events",
		rationale:  "The handler ignores the event timestamp and id, so a captured webhook can be replayed to mark an invoice paid again.",
		suggestion: "Reject events older than a few minutes and store processed event ids.",
		confidence: 0.7, fate: fatePending,
	},
	{
		file: evalDemoExportPath, at: "if (err) console.error(err)", severity: "SUGGESTION", category: "security",
		title:      "Error object logged to the console",
		rationale:  "Logging the raw error could expose internal paths in logs.",
		suggestion: "Use the structured logger and log only the message.",
		confidence: 0.35, fate: fateDismissed,
	},
	{
		file: evalDemoExportPath, at: "const file = `/tmp/export-", severity: "SUGGESTION", category: "security",
		title:      "Predictable temporary file name",
		rationale:  "The export file name is derived from the table name, so another local user could pre-create it.",
		suggestion: "Create the file with `mkdtemp` or a random suffix.",
		confidence: 0.42, fate: fateDismissed,
	},
	{
		file: evalDemoSessionPath, at: "expiresIn: '30d'", severity: "SUGGESTION", category: "security",
		title:      "Session lifetime of 30 days is long",
		rationale:  "A long-lived token stays valid after a role change or logout.",
		suggestion: "Shorten the lifetime and add a refresh flow.",
		confidence: 0.4, fate: fateDismissed,
	},
	{
		file: evalDemoInvoicePath, at: "Math.random()", severity: "WARNING", category: "security",
		title:      "Insecure random number generator for receipt id",
		rationale:  "`Math.random` is not cryptographically secure, so receipt ids may be guessable.",
		suggestion: "Use `crypto.randomUUID()`.",
		confidence: 0.5, fate: fateDismissed,
	},
	{
		file: evalDemoInvoicePath, at: "console.log(`marking invoice", severity: "SUGGESTION", category: "security",
		title:      "Invoice id written to logs",
		rationale:  "Identifiers in logs can aid enumeration if logs are exposed.",
		suggestion: "Drop the log line or move it to debug level.",
		confidence: 0.3, fate: fateDismissed,
	},
}

// lineOf returns the 1-based number of the first added line of the file that contains at; a
// stale anchor is an error, so a finding can never point outside the stored hunk.
func lineOf(path, at string) (int, error) {
	for _, f := range evalDemoFiles {
		if f.path != path {
			continue
		}
		for i, l := range f.lines {
			if strings.Contains(l, at) {
				return i + 1, nil
			}
		}
	}
	return 0, fmt.Errorf("anchor %q not found in %s", at, path)
}

// evalDemoAdditions is the number of added lines over all files.
func evalDemoAdditions() int {
	n := 0
	for _, f := range evalDemoFiles {
		n += len(f.lines)
	}
	return n
}

// evalDemoReview builds the PR #483 review of the given agent, run and findings included.
func evalDemoReview(workspaceID, prID string, agent AgentRef) (DemoReview, error) {
	findings := make([]DemoFinding, len(evalDemoFindings))
	blockers := 0
	for i, f := range evalDemoFindings {
		line, err := lineOf(f.file, f.at)
		if err != nil {
			return DemoReview{}, err
		}
		findings[i] = DemoFinding{
			File: f.file, StartLine: line, EndLine: line, Severity: f.severity, Category: f.category,
			Title: f.title, Rationale: f.rationale, Suggestion: f.suggestion, Confidence: f.confidence,
			Accepted: f.fate == fateAccepted, Dismissed: f.fate == fateDismissed,
		}
		if f.severity == "CRITICAL" {
			blockers++
		}
	}
	n := len(findings)
	return DemoReview{
		WorkspaceID: workspaceID,
		PRID:        prID,
		AgentID:     agent.ID,
		Kind:        "review",
		Verdict:     "request_changes",
		Summary:     evalDemoSummary,
		Score:       evalDemoScore,
		Model:       evalDemoModel,
		Findings:    findings,
		Run: DemoRun{
			Provider:      agent.Provider,
			Model:         agent.Model,
			DurationMs:    evalDemoDurationMs,
			TokensIn:      evalDemoTokensIn,
			TokensOut:     evalDemoTokensOut,
			CostUSD:       evalDemoCostUSD,
			Status:        demoRunStatus,
			Source:        demoRunSource,
			FindingsCount: n,
			Grounding:     fmt.Sprintf("%d/%d passed", n, n),
			Score:         evalDemoScore,
			Blockers:      blockers,
		},
	}, nil
}

// writeEvalDemo seeds PR #483 with its files, its commit and the Security Reviewer review
// (FR6057, D6021). It does nothing when the workspace has no "Security Reviewer" or when
// PR #483 already exists, so a restart never re-creates a demo the user deleted findings or
// runs from, and a database seeded before Phase 6 gets the demo once. It runs after the agents
// and after the PR #482 demo review.
func (s *Seeder) writeEvalDemo(ctx context.Context, tx Tx, workspaceID, repoID string, res *Result) error {
	agent, found, err := tx.FindAgent(ctx, workspaceID, evalDemoAgentName)
	if err != nil {
		return fmt.Errorf("find %s: %w", evalDemoAgentName, err)
	}
	if !found {
		return nil
	}
	prID, inserted, err := tx.EnsurePullRequest(ctx, PullRequest{
		WorkspaceID: workspaceID,
		RepoID:      repoID,
		Number:      evalDemoPRNumber,
		Title:       evalDemoPRTitle,
		Author:      evalDemoPRAuthor,
		Branch:      evalDemoPRBranch,
		Base:        "main",
		HeadSHA:     evalDemoCommitSHA,
		Additions:   evalDemoAdditions(),
		Deletions:   0,
		FilesCount:  len(evalDemoFiles),
		Status:      "needs_review",
		Body:        evalDemoPRBody,
	})
	if err != nil {
		return fmt.Errorf("pull request %d: %w", evalDemoPRNumber, err)
	}
	if !inserted {
		return nil
	}
	res.PullRequests++

	patches := evalDemoPatches()
	for _, f := range evalDemoFiles {
		inserted, _, err := tx.EnsureFile(ctx, prID, File{
			Path: f.path, Additions: len(f.lines), Deletions: 0, Patch: patches[f.path],
		})
		if err != nil {
			return fmt.Errorf("pr file %s: %w", f.path, err)
		}
		res.Files += b2i(inserted)
	}
	inserted, err = tx.EnsureCommit(ctx, prID, Commit{
		SHA: evalDemoCommitSHA, Message: evalDemoCommitMsg, Author: evalDemoPRAuthor,
	})
	if err != nil {
		return fmt.Errorf("pr commit: %w", err)
	}
	res.Commits += b2i(inserted)

	d, err := evalDemoReview(workspaceID, prID, agent)
	if err != nil {
		return fmt.Errorf("eval demo review: %w", err)
	}
	if err := tx.InsertDemoReview(ctx, d); err != nil {
		return fmt.Errorf("eval demo review: %w", err)
	}
	res.Reviews++
	res.Findings += len(d.Findings)
	res.AgentRuns++
	return nil
}
