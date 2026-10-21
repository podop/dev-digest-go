package seed

import "strings"

// pr482Patches returns the unified-diff hunks stored in pr_files.patch for the seeded PR #482
// (acme/payments-api), keyed by file path. The text is copied verbatim from
// SEED_PR_482_PATCHES in the Node server (server/src/db/seed-diff.ts); testdata/pr482_patches.golden.json
// is that object serialised by Node and the tests compare the two byte for byte.
func pr482Patches() map[string]string {
	return map[string]string{
		"src/middleware/ratelimit.ts": lines(
			"@@ -0,0 +1,15 @@",
			"+import type { FastifyRequest, FastifyReply } from 'fastify';",
			"+import { config } from '../config';",
			"+",
			"+const buckets = new Map<string, { tokens: number; updatedAt: number }>();",
			"+",
			"+export async function rateLimit(req: FastifyRequest, reply: FastifyReply) {",
			"+  const key = (req.headers['x-forwarded-for'] as string) ?? req.ip;",
			"+  const now = Date.now();",
			"+  const bucket = buckets.get(key) ?? { tokens: config.rateLimitBurst, updatedAt: now };",
			"+  bucket.tokens = Math.min(config.rateLimitBurst, bucket.tokens + ((now - bucket.updatedAt) / 60_000) * config.rateLimitPerMinute);",
			"+  bucket.updatedAt = now;",
			"+  if (bucket.tokens < 1) return reply.code(429).send({ error: 'rate_limited' });",
			"+  bucket.tokens -= 1;",
			"+  buckets.set(key, bucket);",
			"+}",
		),
		"src/api/public/webhooks.ts": lines(
			"@@ -1,4 +1,6 @@",
			" import { FastifyInstance } from 'fastify';",
			"+import { rateLimit } from '../../middleware/ratelimit';",
			" ",
			" export async function webhooksRoutes(app: FastifyInstance) {",
			"-  app.post('/webhooks/stripe', handleStripe);",
			"+  app.post('/webhooks/stripe', { preHandler: rateLimit }, handleStripe);",
			"+  app.post('/webhooks/github', { preHandler: rateLimit }, handleGithub);",
		),
		"src/config.ts": lines(
			"@@ -9,4 +9,7 @@ export const config = {",
			"   port: Number(process.env.PORT ?? 3000),",
			"   redisUrl: process.env.REDIS_URL,",
			"   logLevel: process.env.LOG_LEVEL ?? 'info',",
			"+  stripeKey: 'sk_live_REDACTED_FOR_DEMO',",
			"+  rateLimitPerMinute: 60,",
			"+  rateLimitBurst: 20,",
			" };",
		),
		"src/api/users.ts": lines(
			"@@ -43,5 +43,8 @@ export async function listUsers(req: FastifyRequest) {",
			"   const users = await db.select().from(usersTable).limit(50);",
			"-  const out = [];",
			"-  for (const u of users) out.push(u);",
			"+  const out = [];",
			"+  for (const u of users) {",
			"+    const orgs = await db.select().from(orgsTable).where(eq(orgsTable.userId, u.id));",
			"+    out.push({ ...u, orgs });",
			"+  }",
			"   return out;",
			" }",
		),
	}
}

// lines joins hunk lines with "\n" and no trailing newline, as the Node source does.
func lines(l ...string) string { return strings.Join(l, "\n") }
