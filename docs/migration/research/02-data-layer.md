# Research: data-layer inventory of `server/` (Drizzle/Postgres16/pgvector) for the Go + GORM migration
Mode: repo · Scope: `<dev-digest>/server/src/{db,modules,platform,adapters}`, `drizzle.config.ts`, `node_modules/drizzle-orm` · Commit: d1c9093
All paths below are relative to `server/`. Sections tagged `[GORM, general knowledge]` are my recommendations and are not verified against GORM docs.

## Answer
The schema is 44 tables, defined in `src/db/schema/*.ts` and re-exported by `src/db/schema.ts`. It comes from 21 Drizzle migrations (0000–0020). Running them in journal order against a DB that already has `CREATE EXTENSION vector` should rebuild the final schema. I did not diff the result against the TS schema or the snapshots.

The `.sql` files are plain Postgres. The `--> statement-breakpoint` markers are SQL line comments, so they are harmless. Postgres 15 or later is required because of `NULLS NOT DISTINCT`.

Data access is plain CRUD, with ON CONFLICT upserts, `FOR UPDATE`, advisory locks, a few aggregations and window functions, and one recursive-free CTE. There is no pgvector similarity query anywhere in the code. The `embedding` columns and HNSW indexes exist, but nothing reads or writes embeddings yet.

Recommendation: keep schema ownership in the existing `.sql` files and do not use GORM AutoMigrate (details in section 6).

## 1. Migrations

### How Drizzle tracks and applies them
| Item | Fact | Evidence |
|---|---|---|
| Runner | `drizzle-orm/postgres-js/migrator` `migrate(db, {migrationsFolder})`, preceded by `CREATE EXTENSION IF NOT EXISTS vector` | `src/db/migrate.ts:21-30` |
| Entry points | `pnpm db:migrate` (`tsx src/db/migrate.ts`) and the Testcontainers harness. Neither `server.ts` nor `app.ts` runs migrations or seed at startup (grep found nothing). | `package.json` scripts; `src/db/migrate.ts:14-16` |
| Tracking table | `drizzle.__drizzle_migrations (id SERIAL PK, hash text NOT NULL, created_at bigint)`, created with `CREATE SCHEMA IF NOT EXISTS drizzle` | `node_modules/drizzle-orm/pg-core/dialect.js:44-55` (drizzle-orm 0.45.3) |
| Hash | sha256 hex of the entire `.sql` file text. `created_at` = journal `when` (ms epoch). | `node_modules/drizzle-orm/migrator.js:21-26` |
| "Applied" logic | Only the newest row is read (`order by created_at desc limit 1`). A migration runs iff `last.created_at < entry.when`. The hash is stored but never compared. | `dialect.js:56-69` |
| Execution | All pending migrations run in ONE transaction. Each file is split on `--> statement-breakpoint` and each chunk is executed separately. | `dialect.js:60-71`; `migrator.js:19` |
| Journal | `src/db/migrations/meta/_journal.json`: version 7, dialect postgresql, 21 entries, all `breakpoints: true`. Tags 0000–0020, `when` ascending (1780354425323 → 1792069488911). `meta/NNNN_snapshot.json` files are drizzle-kit only. | `_journal.json` |
| Config | `drizzle.config.ts`: schema `./src/db/schema.ts`, out `./src/db/migrations`, strict. | `drizzle.config.ts` |
| Extension | `vector` is NOT created by any `.sql` file (grep for `CREATE EXTENSION` finds only `migrate.ts`). It must be created before 0000. The docker image is `pgvector/pgvector:pg16`. | `docker-compose.yml:5`; `src/db/migrate.ts:23` |

### Migration list
| # | File | What it does |
|---|---|---|
| 0000 | `0000_init.sql` (411 lines, 88 breakpoints) | Creates 33 tables: users, workspaces, workspace_members, settings, repos, pull_requests, pr_files, pr_commits, reviews, findings, pr_intent, pr_brief, skills, skill_versions, agents, agent_versions, agent_skills, memory, conventions, code_chunks, symbols, "references", onboarding, eval_cases, eval_runs, conformance_checks, composed_reviews, ci_installations, ci_runs, agent_runs, run_traces, multi_agent_runs, jobs, installed_plugins, digests. It also adds their FKs and indexes, and two `vector(1536)` columns (`:75`, `:190`). |
| 0001 | `0001_add_agent_run_error` | `agent_runs.error text` |
| 0002 | `0002_warm_moonstone` | `agents.strategy text NOT NULL DEFAULT 'single-pass'` |
| 0003 | `0003_minor_overlord` | `agents.ci_fail_on text NOT NULL DEFAULT 'critical'` |
| 0004 | `0004_needy_grey_gargoyle` | Creates `file_edges`, `file_facts`, `repo_index_state`. Adds columns to `references` (decl_file, content_hash) and `symbols` (end_line, exported, signature, content_hash). Adds indexes, including the unique `symbols_repo_path_name_kind_line_uq` (replaced in 0011). |
| 0005 | `0005_gray_peter_parker` | Creates `file_rank` and `repo_map_cache` |
| 0006 | `0006_sharp_mordo` | `agent_runs.score int`, `agent_runs.blockers int` |
| 0007 | `0007_minor_mad_thinker` | `agents.repo_intel bool NOT NULL DEFAULT true` |
| 0008 | `0008_striped_lady_deathstrike` | `reviews.run_id uuid` |
| 0009 / 0010 | `complex_runaways` / `tearful_patch` | Drop `agent_runs.cost_usd`, then re-add it as `double precision`. This is lossy on a populated DB but fine on a fresh one. |
| 0011 | `0011_integrity_indexes_checks` (53 lines) | Hand-added dedupe DELETEs and dangling-reference UPDATEs. Drops the old unique indexes on settings and symbols. Changes `cost_usd` to `numeric(12,6)` on eval_runs, ci_runs, agent_runs. Adds the FKs `reviews.agent_id` (SET NULL) and `reviews.run_id` (CASCADE). Adds many btree indexes plus 2 HNSW indexes (`memory_embedding_hnsw_idx`, `code_chunks_embedding_hnsw_idx`, both `vector_cosine_ops`), a partial index `agent_runs_running_idx`, UNIQUE `NULLS NOT DISTINCT` on settings and symbols, and about 20 `*_chk` CHECK constraints for enum-like text columns. |
| 0012 | `0012_skills` | Creates `agent_run_skills`. Adds findings.skill_id/skill_name, skill_versions.description/message, skills.source_ref/updated_at, `skills_ws_name_uq`, `skills_name_len_chk`. Re-creates `skills_source_chk` to add `imported_file`. |
| 0013 | `0013_conventions_extractor` | Creates `convention_scans`. Runs `DELETE FROM conventions WHERE repo_id IS NULL`, then sets `repo_id` NOT NULL. Adds scan_id, category, evidence, status, edited, skill_id, created_at, updated_at. Adds the partial unique index `convention_scans_one_running_uq`. |
| 0014 | `0014_conventions_drop_legacy` | Drops conventions.evidence_path, evidence_snippet, accepted |
| 0015 | `0015_intent_layer` | `findings.out_of_scope`. Adds 13 columns to `pr_intent` (change_type, confidence, derived_from, sources, head_sha, input_hash, prompt_version, provider, model, tokens_in/out, cost_usd, derived_at). Adds 3 CHECKs. |
| 0016 | `0016_fearless_katie_power` | Creates `agent_context_docs` and `skill_context_docs` |
| 0017 | `0017_lowly_moira_mactaggert` | Creates `context_files` |
| 0018 | `0018_eval_pipeline` | Creates `eval_suite_runs`. Adds eval_cases.created_at/source_*_id, and 8 columns on eval_runs. Adds partial and unique indexes. |
| 0019 | `0019_eval_case_editor` | Replaces the single "one running" unique index with per-owner-kind partial uniques. Adds agent_name (with a backfill UPDATE), owner_kind, skill_id, skill_version, content_edited_at, and 2 CHECKs. |
| 0020 | `0020_lab07_multi_agent_and_ci` | `ci_installations.slug/manifest_version`; `ci_runs.agent_run_id/github_run_id(bigint)/run_attempt/head_sha/manifest_version`; `agent_runs.multi_agent_run_id` (+FK, index); 3 UNIQUE constraints |

### Can a Go migration tool apply them as-is?
- **Statement markers.** `--> statement-breakpoint` begins with `--`, so it is a comment. Postgres accepts the whole file as multi-statement SQL, so golang-migrate over pgx works unchanged. The original files contain no `DO $$` blocks and no `CONCURRENTLY`. Only 0000's start was grepped for `DO $$`, so 0001+ could differ.
- **Naming and directives.** golang-migrate needs `NNNN_name.up.sql` naming and goose needs `-- +goose Up` annotations. Either means copying or renaming the files, not editing their SQL. Alternatively use a custom runner that applies the files in `_journal.json` order and splits on the marker.
- **Transactions.** Drizzle runs everything in one transaction. goose and golang-migrate run per file, which is fine here (no CONCURRENTLY).
- **Existing databases.** A Go tool will not see `drizzle.__drizzle_migrations`. Either baseline it (mark version 20 as applied) or read the Drizzle table. Fresh databases are unaffected.
- **Postgres version.** `NULLS NOT DISTINCT` (0011 settings and symbols) requires PG ≥ 15. The repo targets PG16.
- **Prerequisite.** `CREATE EXTENSION IF NOT EXISTS vector;` must run before 0000. Put it in a Go "migration 0" or in docker init.
- **Reproducibility.** Order is in `_journal.json`, and the numeric prefixes match the journal order. Fresh-run equality with the TS schema was not diffed (see section 7).
- **Future migrations.** Drizzle-kit would stop being the generator once you migrate. The `meta/*_snapshot.json` files become dead weight.

## 2. Final schema
Conventions that apply to every table unless noted:
- `id uuid PK DEFAULT gen_random_uuid()`.
- `created_at timestamptz NOT NULL DEFAULT now()` (`schema/_shared.ts:6`).
- Enum-like columns are `text` plus a CHECK named `<table>_<col>_chk`. They are not native PG enums.
- FKs are `ON UPDATE no action`.
- Cost columns are `numeric(12,6)` with `mode:'number'`, so Node reads them as JS numbers.

`ws` = `workspace_id uuid NOT NULL → workspaces ON DELETE CASCADE`. `[code]` marks values added by hand-written CHECKs.

| Table (file) | Columns (non-obvious) | Keys / uniques / indexes | FKs (on delete) |
|---|---|---|---|
| users (core.ts:6) | email text, name text, created_at | PK id | none |
| workspaces (core.ts:13) | name text, created_at | PK id | none |
| workspace_members (core.ts:23) | ws, user_id, role text default 'member' | PK(workspace_id,user_id); CHECK role IN owner/member | user→users CASCADE |
| settings (core.ts:42) | ws, user_id NULL, key text, **value jsonb NULL** | UNIQUE `settings_ws_user_key_uq`(ws,user_id,key) **NULLS NOT DISTINCT** | ws CASCADE; user CASCADE |
| repos (repos.ts:5) | ws, owner, name, full_name, default_branch default 'main', clone_path NULL, last_polled_at NULL, created_by NULL | UNIQUE idx `repos_ws_fullname_uq`(ws,full_name); idx `repos_ws_idx` | created_by→users (no action) |
| pull_requests (pulls.ts:10) | ws, repo_id, number int, title, author, branch, base, head_sha, last_reviewed_sha NULL, additions/deletions/files_count int default 0, status default 'needs_review', body NULL, opened_at NULL, updated_at NULL | UNIQUE `pr_repo_number_uq`(repo_id,number); idx `pr_ws_idx`; CHECK status IN needs_review/reviewed/stale/open/closed/merged | ws CASCADE; repo CASCADE |
| pr_files (pulls.ts:42) | pr_id, path, additions, deletions default 0, patch text NULL | UNIQUE `pr_files_pr_path_uq`(pr_id,path) | pr CASCADE |
| pr_commits (pulls.ts:59) | pr_id, sha, message, author, committed_at NULL | UNIQUE `pr_commits_pr_sha_uq`(pr_id,sha) | pr CASCADE |
| reviews (reviews.ts:44) | ws, pr_id, agent_id NULL, run_id NULL, kind, verdict NULL, summary NULL, score int NULL, model NULL | idx (pr_id,created_at), run_idx, agent_idx; CHECK kind IN summary/review | ws, pr: CASCADE; agent: **SET NULL**; run→agent_runs: CASCADE |
| findings (reviews.ts:70) | review_id, file, start_line, end_line int, severity, category, title, rationale, suggestion NULL, confidence float8, kind default 'finding', **trifecta_components jsonb NULL**, accepted_at NULL, dismissed_at NULL, skill_id NULL, skill_name NULL, out_of_scope bool default false | idx review, skill; CHECKs severity [CRITICAL,WARNING,SUGGESTION], category [bug,security,perf,style,test], kind [finding,secret_leak,lethal_trifecta,phantom,hook] | review CASCADE; skill SET NULL |
| pr_intent (reviews.ts:121) | **PK pr_id**, intent text, **in_scope jsonb NN default '[]'**, **out_of_scope jsonb NN '[]'**, change_type NULL, confidence NULL, derived_from default 'inferred', **sources jsonb NN '[]'**, head_sha, input_hash, prompt_version int, provider, model, tokens_in/out, cost_usd numeric(12,6), derived_at | CHECKs change_type [feature,bugfix,refactor,docs,test,chore,security,perf,mixed], derived_from [explicit,inferred], confidence [high,medium,low] | pr CASCADE |
| pr_brief (reviews.ts:152) | **PK pr_id**, **json jsonb NN** | | pr CASCADE |
| skills (skills.ts:11) | ws, name, description, type, source, source_ref NULL, body, enabled default true, version default 1, **evidence_files jsonb NULL (string[])**, updated_at | UNIQUE `skills_ws_name_uq`(ws,name); CHECK length(name) 1..64; type [rubric,convention,security,custom]; source [manual,imported_file,imported_url,extracted,community] | ws CASCADE |
| skill_versions (skills.ts:42) | skill_id, version, body, description NULL, message NULL | PK(skill_id,version) | skill CASCADE |
| agents (agents.ts:12) | ws, name, description default '', provider, model, system_prompt, **output_schema jsonb NULL**, strategy default 'single-pass', ci_fail_on default 'critical', repo_intel default true, enabled default true, version default 1, created_by NULL | idx `agents_ws_idx`; CHECKs provider [openai,anthropic,openrouter], strategy [single-pass,map-reduce,auto], ci_fail_on [never,critical,warning,any] | ws CASCADE; created_by→users (no action) |
| agent_versions (agents.ts:52) | agent_id, version, **config_json jsonb NN** | PK(agent_id,version) | agent CASCADE |
| agent_skills (agents.ts:65) | agent_id, skill_id, order int default 0 (reserved word, must be quoted) | PK(agent_id,skill_id); idx skill_id | both CASCADE |
| memory (knowledge.ts:26) | ws, repo_id NULL, scope, kind, content, **embedding vector(1536) NULL**, confidence float8 NULL, **sources jsonb NULL**, updated_at, last_used_at NULL | idx ws, repo; **HNSW (embedding vector_cosine_ops)**; CHECK scope [repo,global,team], kind [decision,convention,preference,fact,learning] | ws, repo CASCADE |
| convention_scans (knowledge.ts:96) | ws, repo_id, status default 'running', **sampled_files jsonb NN '[]' (string[])**, proposed/kept int default 0, **dropped jsonb NN '[]' ({rule,path,reason}[])**, model, tokens_in/out, cost_usd **float8**, error, started_at, finished_at NULL | idx (repo_id, started_at DESC NULLS LAST); **partial UNIQUE (repo_id) WHERE status='running'**; CHECK status [running,done,failed] | ws, repo CASCADE |
| conventions (knowledge.ts:120) | ws, repo_id NOT NULL, scan_id NULL, category default 'other', rule, **evidence jsonb NN '[]' ({path,start_line,end_line,snippet}[])**, confidence float8 NULL, status default 'pending', edited default false, skill_id NULL, updated_at | idx repo; CHECK category [naming,structure,error-handling,async,types,imports,testing,api,data-access,style,other], status [pending,accepted,rejected] | ws, repo CASCADE; scan SET NULL; skill SET NULL |
| code_chunks (context.ts:34) | ws, repo_id, path, content, **embedding vector(1536) NULL**, source default 'code' | idx repo; **HNSW cosine**; CHECK source [code,docs,spec] | ws, repo CASCADE |
| symbols (context.ts:62) | repo_id, path, name, kind, line int NULL, end_line NULL, exported bool default false, signature NULL, content_hash NULL | idx (repo,path), (repo,name); UNIQUE (repo,path,name,kind,line) **NULLS NOT DISTINCT** | repo CASCADE |
| "references" (context.ts:87) | repo_id, from_path, to_symbol, line int NN, decl_file NULL, content_hash NULL | idx (repo,decl_file,to_symbol), (repo,from_path) | repo CASCADE |
| onboarding (context.ts:112) | **PK repo_id**, **json jsonb NN**, generated_at | | repo CASCADE |
| eval_cases (eval.ts:41) | ws, owner_kind, owner_id uuid (**no FK**, polymorphic), name, input_diff NULL, **input_files jsonb NULL**, **input_meta jsonb NULL**, **expected_output jsonb NULL**, notes, source_finding_id/source_review_id/source_pr_id uuid NULL (no FK), content_edited_at NULL | UNIQUE idx (source_finding_id); idx (owner_kind,owner_id,created_at); CHECK owner_kind [skill,agent] | ws CASCADE |
| eval_suite_runs (eval.ts:68) | ws, agent_id, agent_version, agent_name default '', owner_kind default 'agent', skill_id NULL, skill_version NULL, **config jsonb NN (EvalRunConfigSnapshot)**, **case_ids jsonb NN (string[])**, status default 'running', started_at, finished_at, cases_total/finished/passed/errored default 0, recall/precision/citation_accuracy float8 NULL, unlabeled_findings default 0, cost_usd numeric, duration_ms, error | partial UNIQUE (agent_id) WHERE running AND owner_kind='agent'; partial UNIQUE (skill_id) WHERE running AND owner_kind='skill'; idx (agent,started_at), (skill,started_at), (ws,started_at); CHECKs status [running,completed,failed], owner_kind, owner_consistency | ws, agent, skill: CASCADE |
| eval_runs (eval.ts:130) | case_id, ran_at, **actual_output jsonb NULL**, pass bool NULL, recall/precision/citation_accuracy float8, duration_ms, cost_usd numeric, run_id NULL, status NULL, error, dropped/hit/violation/unlabeled_count int default 0 | idx case; UNIQUE (run_id,case_id); CHECK status [passed,failed,error] | case CASCADE; run→eval_suite_runs CASCADE |
| conformance_checks (eval.ts:162) | pr_id, spec_id text, completeness_pct float8, **items jsonb NULL** | | pr CASCADE |
| composed_reviews (eval.ts:171) | pr_id, body NN, verdict, posted_at, github_review_id text | | pr CASCADE |
| ci_installations (ci.ts:8) | agent_id, repo text, target_type, installed_at, slug NULL, manifest_version NULL | UNIQUE (agent_id,repo,target_type); UNIQUE (repo,slug); CHECK target_type [gha,circle,jenkins,cli] | agent CASCADE |
| ci_runs (ci.ts:28) | ci_installation_id NULL, pr_number, ran_at, status, findings_count, cost_usd numeric, github_url, source (all nullable), agent_run_id NULL, **github_run_id bigint (mode number)**, run_attempt int, head_sha, manifest_version | UNIQUE (github_run_id, run_attempt) | installation SET NULL; agent_run SET NULL |
| agent_runs (runs.ts:14) | ws, agent_id NULL, pr_id NULL, ran_at, provider, model, duration_ms, tokens_in/out, cost_usd numeric(12,6) NULL, status NULL, error, source default 'local', findings_count, grounding text, score, blockers, multi_agent_run_id NULL | idx (pr_id,ran_at); **partial idx (pr_id) WHERE status='running'**; idx multi_agent_run_id; CHECK status [running,done,failed,cancelled], source [local,ci] | ws CASCADE; agent SET NULL; pr SET NULL; multi_agent_run SET NULL |
| agent_run_skills (runs.ts:68) | run_id, skill_id, skill_version, order int NN | PK(run_id,skill_id); idx skill_id | both CASCADE |
| run_traces (runs.ts:84) | **PK run_id**, **trace jsonb NN** (whole run log; TS type `RunTrace` in `reviews/domain/trace.ts`, not opened) | | run CASCADE |
| multi_agent_runs (runs.ts:89) | ws, pr_id, ran_at | | ws, pr CASCADE |
| jobs (ops.ts:10) | ws, kind, **payload jsonb NULL**, status default 'queued', attempts default 0, scheduled_at default now, started_at, finished_at, error | idx status, idx ws; CHECK status [queued,running,done,failed] | ws CASCADE |
| installed_plugins (ops.ts:35) | ws, name, version, source, installed_at, enabled default true | | ws CASCADE |
| digests (ops.ts:46) | ws, period_start, period_end, body_md, delivered_to | | ws CASCADE |
| repo_index_state (repo-intel.ts:19) | **PK repo_id**, last_indexed_sha, indexer_version int, status, files_indexed/skipped default 0, **stats jsonb NN '{}'**, updated_at | CHECK status [full,partial,degraded,failed] | repo CASCADE |
| file_edges (repo-intel.ts:36) | repo_id, from_file, to_file | PK(repo,from,to); idx (repo,to_file) | repo CASCADE |
| file_facts (repo-intel.ts:53) | repo_id, file_path, **endpoints jsonb NN '[]'**, **crons jsonb NN '[]'** | PK(repo,file_path) | repo CASCADE |
| file_rank (repo-intel.ts:70) | repo_id, file_path, pagerank/hotness/rank float8 NN, percentile **smallint** NN | PK(repo,file_path); idx (repo,rank) | repo CASCADE |
| repo_map_cache (repo-intel.ts:90) | repo_id, commit_sha, token_budget int, map_text, token_count, created_at | PK(repo,commit_sha,token_budget) | repo CASCADE |
| agent_context_docs (project-context.ts:21) | agent_id, repo_id, path, position int, created_at | PK(agent,repo,path); idx repo; CHECK length(path) 1..512 | both CASCADE |
| skill_context_docs (project-context.ts:44) | skill_id, repo_id, path, position, created_at | PK(skill,repo,path); idx repo; CHECK path len | both CASCADE |
| context_files (project-context.ts:66) | repo_id, path, content, size_bytes int, version default 1, updated_at | UNIQUE (repo_id,path); CHECK path len | repo CASCADE |

Other schema facts:
- **PG enums:** none; every enum is text plus CHECK.
- **pgvector:** `memory.embedding` and `code_chunks.embedding` are `vector(1536)`, nullable, with HNSW indexes using `vector_cosine_ops` (`schema/knowledge.ts:46`, `schema/context.ts:51-52`). The embedder is OpenAI text-embedding-3-small, gated by `EMBEDDINGS_ENABLED` (`platform/config.ts:20-23`).
- **PG arrays:** none. Lists are stored as jsonb.
- **JSONB typing in TS:** `string[]` for skills.evidence_files, findings.trifecta_components, pr_intent.in_scope/out_of_scope, convention_scans.sampled_files and eval_suite_runs.case_ids. `IntentSource[]` for pr_intent.sources. `DroppedConventionRow[]`, `ConventionEvidenceRow[]` (snake_case keys) and `EvalRunConfigSnapshot` (`schema/eval.ts`) for their columns. Untyped (`unknown`) jsonb columns are validated in mappers with Zod `.parse` (e.g. `evals/infrastructure/mappers.ts:18-20,118`, `agents/infrastructure/mappers.ts:38`).
- **Tenancy:** most tables have `workspace_id`; the child tables inherit it through joins. `context_files`, `symbols` and the repo-intel tables are scoped by `repo_id` only.

## 3. Access patterns per module
Tables are listed by reading `t.<table>` references in each module's repository files.

| Module (repository file) | Tables touched | Notable queries |
|---|---|---|
| agents (`agents/repository.ts`) | agents, agent_skills, agent_versions, skills, eval_cases | `SELECT … FOR UPDATE` on the agent row inside `atomically`/transaction (`:43,139,264`). Version bump on config change. `onConflictDoNothing` on skill links (`:193`). |
| skills (`skills/infrastructure/repository.ts`) | skills, skill_versions, agent_skills, agent_runs, agent_run_skills, findings, reviews, eval_cases, agents | Correlated subquery `usedBy` (`:18`), `ORDER BY name collate "C"` (`:53`), `count(distinct …)` stats (`:100,107`), `FOR UPDATE` on skills (`:199`). |
| reviews (`reviews/repository/*.ts`) | reviews, findings, agent_runs, agent_run_skills, run_traces, multi_agent_runs, memory, pull_requests, repos, pr_files, eval_cases, agents, skills | `run.repo.ts:225-236`: conditional UPDATE with `CASE WHEN status='running'` plus `coalesce(error,…)` and `RETURNING`. `:245` upsert run_traces on run_id. `:273` ON CONFLICT DO NOTHING on agent_run_skills. `multi-agent.repo.ts:127-166`: `count … filter`, `sum(cost)::float8`, `row_number() over (partition by agent_id order by ran_at desc, id)`, `avg`. `:201` `pg_advisory_xact_lock(hashtextextended('learn:<id>',0))`. `:218` jsonb `sources @> ANY(ARRAY[…::jsonb])`. `:232-242` insert into memory with no embedding. |
| pulls (`pulls/repository.ts`) | pull_requests, pr_files, pr_commits, repos, reviews, findings, agent_runs | Three upserts (`:93,146,178`): `ON CONFLICT (repo_id,number)` and the (pr_id,path) / (pr_id,sha) uniques, using `excluded.*`. `sum(cost_usd)` per PR (`:259`). |
| polling / repos / workspace | repos | Plain CRUD and `last_polled_at` updates. `adapters/auth/local.ts` uses users and workspaces. |
| settings (`settings/repository.ts:23`) | settings | `INSERT … ON CONFLICT (workspace_id,user_id,key) DO UPDATE SET value=excluded.value`. Relies on NULLS NOT DISTINCT. |
| intent (`intent/infrastructure/repository.ts:48`) | pr_intent, pull_requests, pr_files, pr_commits, repos | Upsert on pr_id. |
| brief (`brief/infrastructure/repository.ts:95`) | pr_brief and read joins on reviews, findings, skills, agent_skills | Upsert on pr_id. |
| onboarding (`:45`) | onboarding, repos | Upsert on repo_id. |
| conventions (`conventions/infrastructure/repository.ts`) | conventions, convention_scans, repos | Order via `CASE status` and `confidence desc nulls last` (`:21,53`). Transaction (`:104`): delete pending, un-edited conventions, then bulk insert. Relies on the one-running-scan partial unique. |
| evals (`evals/infrastructure/*.ts`) | eval_cases, eval_runs, eval_suite_runs, agents, findings, reviews, pull_requests, pr_files, repos | `FOR UPDATE` on eval_cases (`repository.ts:180`). `coalesce(content_edited_at, now())` (`:192`). `row_number()` window over eval_suite_runs (`run-queries.ts:153`). `exists(…)` (`:194`). `recordCase` (`:253`): insert eval_runs plus counter increment `col = col + 1` in one tx; it catches FK violation 23503. Uses `greatest(…,0)`. |
| ci (`ci/infrastructure/repository.ts`) | ci_installations, ci_runs, agent_runs, agents | Upsert on (agent_id,repo,target_type) (`:114`). On 23505 on (repo,slug) it returns `slug_taken` (`:120-127`). `recordRun` (`:136`): tx with `ON CONFLICT DO NOTHING RETURNING id` on (github_run_id,run_attempt), then insert agent_runs. |
| performance (`performance/infrastructure/repository.ts`) | agent_runs, agent_run_skills, agents, findings, pull_requests, reviews, runTraces, skills | `count(*) filter (where accepted_at is not null)` aggregates (`:73-75`). `trace -> 'memory_pulled'` jsonb path (`:116`). |
| smart-diff / blast | findings, pr_files, pull_requests, reviews, repos | Read-only joins. |
| repo-intel (`repo-intel/infrastructure/repository.ts`, `read-repository.ts`) | repo_index_state, file_edges, file_facts, file_rank, repo_map_cache, symbols, "references", repos | `pg_advisory_xact_lock(hashtextextended('repo-intel:<id>',0))` (`:43`). Transaction/savepoint (`:34-38`). Chunked bulk inserts, delete-then-insert replace. Upserts on repo_index_state (`:91`) and repo_map_cache (`:186`). Raw `UPDATE "references" … WITH cand/uniq CTE` resolving decl_file (`:152-175`). |
| project-context (`project-context/infrastructure/repository.ts`) | agent_context_docs, skill_context_docs, context_files, agents, skills, repos | `FOR UPDATE` on owner row first (`:124,136,182`). `length(content)`, `ORDER BY path COLLATE "C"` (`:152,158`). `version = version + 1` (`:209,230`). |
| platform/jobs (`platform/jobs.ts:70-79`) | jobs | Insert returning id, then update status. |

**Vector similarity:** no `<=>`, `cosineDistance` or `<->` in `src/` (grep). `code_chunks` has no reader or writer outside the schema file, and `memory` is only inserted by Learn without an embedding. The vector search is therefore not implemented yet; only the schema is ready.

**Transactions**
- Port: `application/transaction.ts` `TransactionRunner<R>.run(work)`. Implementation: `db/transaction.ts:9-17` calls `db.transaction(tx => work(bind(tx)))`, rebinding repositories to `tx`. It is wired in `platform/container.ts:154`.
- Users of the port: repo-intel, pulls, skills, reviews (ReviewTx and LearnTx), project-context, evals.
- Direct `db.transaction` use: agents `atomically` (`agents/repository.ts:43`), conventions (`:104`), ci `recordRun` (`:136`), evals `recordCase` (`run-queries.ts:253`), repo-intel (`:34`), and seeds (`seed.ts:270,328`; `seed-eval-demo.ts:318`).
- The nested "savepoint" call in repo-intel (`:37`) relies on Drizzle turning a nested transaction into a savepoint.
- Database error codes are inspected in the app via `pgCode(err)` (23505 unique, 23503 FK). The helper lives in the infrastructure code; I did not open it.

## 4. Seeds
`db:seed` runs `tsx src/db/seed.ts`. Seed is not called on startup; only tests and the CLI use it (`seed.ts:239-255` is the CLI entry). All seed data lives in TS code with no data files (the repo has `docs/agent-prompts/*.md` mirrors, mentioned in `seed.ts` comments; not read).

| Item | Where | Idempotency |
|---|---|---|
| Workspace `default`, user `you@local`/'You', owner membership | `seed.ts:44-70` | select-then-insert; membership ON CONFLICT DO NOTHING |
| Settings: polling_interval_min=5, theme='dark', density='regular', sync_to_folder=true (per user) | `seed.ts` (default settings block) | ON CONFLICT DO NOTHING |
| Demo repo `acme/payments-api` (main), PR #482 "Add rate limiting to public API endpoints", pr_files, pr_commits, one sample review (model 'seed') and findings | `seed.ts` | guarded by existence checks |
| PR #482 diff patches | `seed-diff.ts` (`SEED_PR_482_PATCHES`) | backfilled only where `patch IS NULL` |
| 4 built-in agents: General, Security, Performance, Test Quality (openrouter, `deepseek/deepseek-v4-flash`) | `seed.ts` agents block; prompts in `seed-prompts.ts` (4 long template strings at lines 11, 93, 189, 294) | insert only if absent |
| 9 built-in skills: pr-quality-rubric, no-then-chains, secret-leakage-gate, lethal-trifecta, phantom-api-gate, n-plus-one-gate, test-coverage-nudge, mock-discipline, corner-case-hunter, plus `SEED_AGENT_SKILLS` agent→skill links | `seed-skills.ts:20-229`; written in a transaction to skills and skill_versions | keyed by name; links written only for an agent with no links |
| Finished agent_run for the sample review, with `review.run_id` set | `seed.ts` `seedPr482Run` | keyed on `run_id IS NULL` |
| Eval demo PR #483 (Security Reviewer) with files, commit, run, review, labelled findings | `seed-eval-demo.ts` | existing-PR check |

Other tables start empty (comment at `seed.ts:35`). The `seed.ts` comment says "three starter presets" but 4 agents are seeded.

## 5. Row ↔ contract mapping
- `src/db/rows.ts:1-21` only exports Drizzle inferred types (`$inferSelect`): AgentRow, AgentVersionRow, FindingRow, PullRow, AgentRunRow, SkillRow, SkillVersionRow, ConventionRow, ConventionScanRow, PrIntentRow. There is no mapping code in `rows.ts`.
- The mapping layer is per-module `mappers.ts` files: `agents/infrastructure`, `ci/infrastructure`, `conventions/infrastructure`, `evals/infrastructure`, `intent/infrastructure`, `reviews/repository`, `skills/infrastructure`. Other modules map inline.
- **Casing:** DB columns are snake_case. Drizzle properties are camelCase (`startLine`). The API/DTO wire contract is **snake_case** (`start_line`, `system_prompt`, `ci_fail_on`, `created_at`), via `@devdigest/shared` types. Examples: `reviews/repository/mappers.ts:11-31`, `agents/infrastructure/mappers.ts:11-26`. So a Go struct can carry both a `gorm:"column:start_line"` tag and a `json:"start_line"` tag.
- **Time:** timestamptz is serialized with `toISOString()` (UTC, ms, `Z`). Nullable ones use `?.toISOString() ?? null`, and `intent` maps a null `derived_at` to epoch 0 (`intent/infrastructure/mappers.ts:27`).
- **Nulls:** columns are mapped null to null (`suggestion ?? null`); the `kind` default is `'finding'`.
- **Numeric:** `numeric(12,6)` cost columns are returned as JS `number`.
- **Renames:** the finding API field `skill` comes from `skill_name`, and `eval_case_id` is joined in from eval_cases. Eval API names (`expectation`, `input_meta`) correspond to `expected_output`/`input_meta` columns.
- **JSON validation:** jsonb is validated at read time by Zod (`AgentVersionConfig.parse`, `EvalExpectation.parse`, `EvalCaseInputMeta.parse`, `EvalResultFinding.array().parse`). Go must keep the same stored JSON shapes, including snake_case keys inside jsonb.
- The shared contract types come from `src/vendor/shared` (`@devdigest/shared`). I did not diff the vendored copy against the original package.

## 6. GORM mapping and recommendations
All of this is `[GORM, general knowledge, not verified]`; I checked no GORM docs, so confirm against the versions you pick.

**Ownership of the schema: keep the `.sql` files as the source of truth and do not use `AutoMigrate`.**
- AutoMigrate cannot create or reproduce: HNSW with `vector_cosine_ops`, `NULLS NOT DISTINCT` uniques, partial indexes and partial uniques, `DESC NULLS LAST` indexes, the CHECK constraints, or `ON DELETE SET NULL/CASCADE` exactly as defined.
- It also risks altering the live Drizzle-created database.
- Use golang-migrate, goose or a small custom runner that applies the files in journal order. Baseline an existing DB at version 20.
- Add `CREATE EXTENSION IF NOT EXISTS vector` first.
- Set `gorm.Config{}` so GORM never writes DDL: `DisableAutomaticPing` off is fine, but do not call `AutoMigrate`/`Migrator`.
- Use GORM models for CRUD only, with explicit `gorm:"column:…;type:…"` tags.
- Add an integration test that applies the SQL to a Testcontainers pgvector image and checks the models against it.

**Per-column mapping**
| Postgres | Go / GORM |
|---|---|
| `uuid PK default gen_random_uuid()` | `ID uuid.UUID` with `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`. A zero value is skipped on insert so the DB generates it; read it back with RETURNING (the postgres driver does this). |
| text + CHECK enum | `string` or a named string type. No Go enum registration needed. Use `*string` for nullable. |
| `timestamptz NOT NULL default now()` | `time.Time` with `gorm:"default:now()"`. Do NOT use GORM's `autoCreateTime` unless you want Go to set it. Nullable columns use `*time.Time`. Keep `updated_at` defaults for insert but remember Drizzle never auto-updates it, so set it explicitly if the app logic requires it (as `context_files` and `skills` do manually). |
| `int`, `smallint`, `bigint` | `int32`/`int`, `int16`, `int64` (github_run_id is bigint) |
| `float8` | `float64`, nullable `*float64` |
| `numeric(12,6)` (cost_usd) | `*float64` with `type:numeric(12,6)` matches the Node "number" behaviour. Use `shopspring/decimal` only if you need exactness. |
| `jsonb NOT NULL default '[]'/'{}'` | `datatypes.JSONType[[]string]` or `datatypes.JSONType[T]` for the typed ones (EvalRunConfigSnapshot, ConventionEvidenceRow, IntentSource). Otherwise `datatypes.JSON`. |
| `jsonb NULL` (settings.value, agents.output_schema, eval_cases.*, memory.sources, jobs.payload, conformance.items, skills.evidence_files, findings.trifecta_components) | Pointer type (`*datatypes.JSON`, `*datatypes.JSONType[T]`) to distinguish SQL NULL from JSON `null`. Check how a NULL value round-trips before relying on it. |
| `vector(1536)` | `pgvector.Vector` (pgvector-go), `gorm:"type:vector(1536)"`; nullable as `*pgvector.Vector`. Register pgvector types on the pgx connection if you use the pgx driver. |
| Composite PKs | Tag each field `gorm:"primaryKey"` (workspace_members, agent_versions, skill_versions, agent_skills, agent_run_skills, file_edges, file_facts, file_rank, repo_map_cache, agent_context_docs, skill_context_docs). Mark non-auto fields `autoIncrement:false` to avoid surprises. |
| Single-column PK that is also an FK (pr_intent, pr_brief, onboarding, run_traces, repo_index_state) | `primaryKey` UUID field, no default. |
| Reserved or quoted names | `"references"` table (set `TableName()` and rely on GORM's quoting) and the `order` column in agent_skills/agent_run_skills. |
| Unique/partial indexes | Declared in SQL only; GORM tags are optional documentation. |
| Relations | Skip GORM association tags where possible. Cascade/SET NULL is done by the DB. Use explicit joins (`Joins`, `Preload` only where simple) to keep query shape equal to the Drizzle code. |

Table to model: 44 tables map one to one to 44 model structs (names as in section 2). `TableName()` must return the snake_case plural names; set `NamingStrategy{SingularTable:false}` or define `TableName()` explicitly, because several table names don't follow GORM's pluralization (`pr_intent`, `onboarding`, `memory`, `file_rank`, `"references"`).

**Queries GORM expresses poorly**
| Pattern in this repo | GORM approach |
|---|---|
| Vector similarity (not yet used) | `db.Raw("… ORDER BY embedding <=> ? LIMIT ?", vec, n)` or `clause.OrderBy{Expression: clause.Expr{SQL: "embedding <=> ?", Vars: …}}`. pgvector-go also ships GORM examples. |
| Upserts (`settings`, `pulls`, `pr_files`, `pr_commits`, `pr_intent`, `pr_brief`, `onboarding`, `run_traces`, `repo_index_state`, `repo_map_cache`, `ci_installations`) | `clause.OnConflict{Columns: […], DoUpdates: clause.AssignmentColumns([…])}`. For `excluded.*` expressions use `clause.Assignments`/`gorm.Expr("excluded.title")`. |
| `ON CONFLICT DO NOTHING` (agent_skills, agent_run_skills, ci_runs, workspace_members, settings seed) | `clause.OnConflict{DoNothing: true}`. For RETURNING detection in `recordRun`, check `RowsAffected == 0` rather than scanning RETURNING. |
| Partial unique targets (e.g. `convention_scans_one_running_uq`, eval_suite_runs "one running") | These are only violated, not targeted by ON CONFLICT here. Catch SQLSTATE 23505 via `errors.As(err, &pgconn.PgError)` (pgx driver). Treat 23503 for the FK case in `recordCase`. |
| Row locking (`FOR UPDATE`) | `db.Clauses(clause.Locking{Strength: "UPDATE"})` inside a transaction. |
| Advisory locks | `tx.Exec("select pg_advisory_xact_lock(hashtextextended(?, 0))", key)` |
| Transactions and savepoints | `db.Transaction(func(tx *gorm.DB) error {…})`. Nested `Transaction` calls use SAVEPOINT by default, like Drizzle. Port `TransactionRunner` as an interface taking a repo-binding function over `*gorm.DB`. |
| Aggregations and window functions (`count … filter`, `sum(cost)::float8`, `row_number() over (partition by …)`, `avg`, correlated subqueries, CASE ordering, `COLLATE "C"`) | Use `Select("…")` / `Raw` with `Scan` into DTO structs. Do not try to express in the query builder. |
| Counter updates (`cases_finished + 1`, `version + 1`, `greatest(cases_total - 1, 0)`) | `Update("col", gorm.Expr("col + ?", 1))` |
| `CASE WHEN status='running' THEN … ELSE … END` conditional update with RETURNING | `Clauses(clause.Returning{})` + `gorm.Expr`, or Raw. |
| jsonb ops (`@> ANY(ARRAY[…::jsonb])`, `trace -> 'memory_pulled'`) | Raw SQL fragments in `Where`/`Select`. |
| CTE UPDATE (`resolveReferences`) | `Exec` raw, unchanged. |
| Bulk inserts in chunks | `CreateInBatches(slice, n)` (mind Postgres' 65,535-parameter limit; the repo-intel code chunks via a `chunked` helper, e.g. `repo-intel/infrastructure/repository.ts:121`). |
| Zero-value pitfalls | `Updates(struct)` skips zero values (`false`, `0`, `""`). Use `Updates(map[string]any)` or `Select(...)` for booleans like `enabled`, `edited`, `repo_intel`, `out_of_scope`. |
| Default-tag pitfall | A Go zero value on a column with a `default:` tag is replaced by the DB default on Create (e.g. `enabled=false` would become true). Use pointer fields or `Select` for such columns. |

## 7. Not found / unclear
- **Fresh-run equality with the TS schema:** I did not apply the SQL or diff it against the `meta/*_snapshot.json` or TS schema. The final-schema table above is built from the TS files (primary) plus the migrations; the two should agree but I did not check column-by-column. I did not read the rest of `0000_init.sql` beyond greps, so index names/FK actions created there come from the TS schema. Column ordering and some default expressions in 0000 are unverified.
- **Other `DO $$` blocks:** only 0000's header region was checked for `DO $$`/CREATE TYPE (none found); I read 0001–0020 fully, and they have none.
- **Vector similarity search:** no implementation to port exists. `code_chunks` is unused by application code; `memory.embedding` is never written. I could not confirm planned query shapes (check `docs/` and `specs/`, which I did not open).
- **`RunTrace` jsonb shape** (`reviews/domain/trace.ts`) and the other `@devdigest/shared` types (`src/vendor/shared`) were not read; the Go DTO/JSON field list must come from there.
- **`pgCode()` helper** location and exact list of handled SQLSTATEs were not inspected.
- **GORM behaviour claims** in section 6 are from general knowledge and not verified against GORM, gorm.io/datatypes or pgvector-go docs/versions.
- **`jobs` handlers in `platform/jobs.ts`:** only the insert/update statements were seen (`:70-79`); there is no evidence of `SKIP LOCKED` polling (grep found none), so the queue is in-process (p-queue) and the table is just a mirror.
- **Whether the app's shared Zod column enums** (`@devdigest/shared`) exactly equal the DB CHECK lists. Code comments claim a contract test keeps them in sync (`schema/reviews.ts:25`), not verified.
- **Data migration of existing DBs:** I did not look for any production data or tooling to move `drizzle.__drizzle_migrations` rows.

Key files (absolute):
- `<dev-digest>/server/src/db/migrations/` (SQL plus `meta/_journal.json`)
- `<dev-digest>/server/src/db/schema/` and `schema.ts`
- `<dev-digest>/server/src/db/migrate.ts`, `transaction.ts`, `rows.ts`, `seed*.ts`
- `<dev-digest>/server/node_modules/drizzle-orm/pg-core/dialect.js` and `migrator.js`
