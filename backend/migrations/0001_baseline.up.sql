-- Baseline (version 1): the final schema of the Node migrations 0000-0020 plus the pgvector
-- extension. Generated from a scratch pgvector/pgvector:pg16 database migrated by the Node
-- migrations with `pg_dump --schema-only --no-owner --no-privileges`; SET statements, psql
-- meta-commands and the search_path reset were removed so the runner session keeps its settings.
-- Later schema changes are new versions; never edit this file.

--
-- Name: vector; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;

--
-- Name: agent_context_docs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_context_docs (
    agent_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    path text NOT NULL,
    "position" integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_context_docs_path_len_chk CHECK (((length(path) >= 1) AND (length(path) <= 512)))
);

--
-- Name: agent_run_skills; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_run_skills (
    run_id uuid NOT NULL,
    skill_id uuid NOT NULL,
    skill_version integer NOT NULL,
    "order" integer NOT NULL
);

--
-- Name: agent_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid,
    pr_id uuid,
    ran_at timestamp with time zone DEFAULT now() NOT NULL,
    provider text,
    model text,
    duration_ms integer,
    tokens_in integer,
    tokens_out integer,
    status text,
    source text DEFAULT 'local'::text NOT NULL,
    findings_count integer,
    grounding text,
    error text,
    score integer,
    blockers integer,
    cost_usd numeric(12,6),
    multi_agent_run_id uuid,
    CONSTRAINT agent_runs_source_chk CHECK ((source = ANY (ARRAY['local'::text, 'ci'::text]))),
    CONSTRAINT agent_runs_status_chk CHECK ((status = ANY (ARRAY['running'::text, 'done'::text, 'failed'::text, 'cancelled'::text])))
);

--
-- Name: agent_skills; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_skills (
    agent_id uuid NOT NULL,
    skill_id uuid NOT NULL,
    "order" integer DEFAULT 0 NOT NULL
);

--
-- Name: agent_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_versions (
    agent_id uuid NOT NULL,
    version integer NOT NULL,
    config_json jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: agents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    system_prompt text NOT NULL,
    output_schema jsonb,
    enabled boolean DEFAULT true NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    strategy text DEFAULT 'single-pass'::text NOT NULL,
    ci_fail_on text DEFAULT 'critical'::text NOT NULL,
    repo_intel boolean DEFAULT true NOT NULL,
    CONSTRAINT agents_ci_fail_on_chk CHECK ((ci_fail_on = ANY (ARRAY['never'::text, 'critical'::text, 'warning'::text, 'any'::text]))),
    CONSTRAINT agents_provider_chk CHECK ((provider = ANY (ARRAY['openai'::text, 'anthropic'::text, 'openrouter'::text]))),
    CONSTRAINT agents_strategy_chk CHECK ((strategy = ANY (ARRAY['single-pass'::text, 'map-reduce'::text, 'auto'::text])))
);

--
-- Name: ci_installations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ci_installations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    agent_id uuid NOT NULL,
    repo text NOT NULL,
    target_type text NOT NULL,
    installed_at timestamp with time zone DEFAULT now() NOT NULL,
    slug text,
    manifest_version integer,
    CONSTRAINT ci_installations_target_type_chk CHECK ((target_type = ANY (ARRAY['gha'::text, 'circle'::text, 'jenkins'::text, 'cli'::text])))
);

--
-- Name: ci_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ci_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    ci_installation_id uuid,
    pr_number integer,
    ran_at timestamp with time zone,
    status text,
    findings_count integer,
    cost_usd numeric(12,6),
    github_url text,
    source text,
    agent_run_id uuid,
    github_run_id bigint,
    run_attempt integer,
    head_sha text,
    manifest_version integer
);

--
-- Name: code_chunks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.code_chunks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    path text NOT NULL,
    content text NOT NULL,
    embedding public.vector(1536),
    source text DEFAULT 'code'::text NOT NULL,
    CONSTRAINT code_chunks_source_chk CHECK ((source = ANY (ARRAY['code'::text, 'docs'::text, 'spec'::text])))
);

--
-- Name: composed_reviews; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.composed_reviews (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    pr_id uuid NOT NULL,
    body text NOT NULL,
    verdict text,
    posted_at timestamp with time zone,
    github_review_id text
);

--
-- Name: conformance_checks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.conformance_checks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    pr_id uuid NOT NULL,
    spec_id text NOT NULL,
    completeness_pct double precision,
    items jsonb
);

--
-- Name: context_files; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.context_files (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repo_id uuid NOT NULL,
    path text NOT NULL,
    content text NOT NULL,
    size_bytes integer NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT context_files_path_len_chk CHECK (((length(path) >= 1) AND (length(path) <= 512)))
);

--
-- Name: convention_scans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.convention_scans (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    sampled_files jsonb DEFAULT '[]'::jsonb NOT NULL,
    proposed integer DEFAULT 0 NOT NULL,
    kept integer DEFAULT 0 NOT NULL,
    dropped jsonb DEFAULT '[]'::jsonb NOT NULL,
    model text,
    tokens_in integer,
    tokens_out integer,
    cost_usd double precision,
    error text,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    CONSTRAINT convention_scans_status_chk CHECK ((status = ANY (ARRAY['running'::text, 'done'::text, 'failed'::text])))
);

--
-- Name: conventions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.conventions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    rule text NOT NULL,
    confidence double precision,
    scan_id uuid,
    category text DEFAULT 'other'::text NOT NULL,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    edited boolean DEFAULT false NOT NULL,
    skill_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT conventions_category_chk CHECK ((category = ANY (ARRAY['naming'::text, 'structure'::text, 'error-handling'::text, 'async'::text, 'types'::text, 'imports'::text, 'testing'::text, 'api'::text, 'data-access'::text, 'style'::text, 'other'::text]))),
    CONSTRAINT conventions_status_chk CHECK ((status = ANY (ARRAY['pending'::text, 'accepted'::text, 'rejected'::text])))
);

--
-- Name: digests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.digests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    period_start timestamp with time zone,
    period_end timestamp with time zone,
    body_md text,
    delivered_to text
);

--
-- Name: eval_cases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.eval_cases (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    owner_kind text NOT NULL,
    owner_id uuid NOT NULL,
    name text NOT NULL,
    input_diff text,
    input_files jsonb,
    input_meta jsonb,
    expected_output jsonb,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    source_finding_id uuid,
    source_review_id uuid,
    source_pr_id uuid,
    content_edited_at timestamp with time zone,
    CONSTRAINT eval_cases_owner_kind_chk CHECK ((owner_kind = ANY (ARRAY['skill'::text, 'agent'::text])))
);

--
-- Name: eval_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.eval_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    case_id uuid NOT NULL,
    ran_at timestamp with time zone DEFAULT now() NOT NULL,
    actual_output jsonb,
    pass boolean,
    recall double precision,
    "precision" double precision,
    citation_accuracy double precision,
    duration_ms integer,
    cost_usd numeric(12,6),
    run_id uuid,
    status text,
    error text,
    dropped_count integer DEFAULT 0 NOT NULL,
    hit_count integer DEFAULT 0 NOT NULL,
    violation_count integer DEFAULT 0 NOT NULL,
    unlabeled_count integer DEFAULT 0 NOT NULL,
    CONSTRAINT eval_runs_status_chk CHECK ((status = ANY (ARRAY['passed'::text, 'failed'::text, 'error'::text])))
);

--
-- Name: eval_suite_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.eval_suite_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    agent_version integer NOT NULL,
    config jsonb NOT NULL,
    case_ids jsonb NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    cases_total integer DEFAULT 0 NOT NULL,
    cases_finished integer DEFAULT 0 NOT NULL,
    cases_passed integer DEFAULT 0 NOT NULL,
    cases_errored integer DEFAULT 0 NOT NULL,
    recall double precision,
    "precision" double precision,
    citation_accuracy double precision,
    unlabeled_findings integer DEFAULT 0 NOT NULL,
    cost_usd numeric(12,6),
    duration_ms integer,
    error text,
    agent_name text DEFAULT ''::text NOT NULL,
    owner_kind text DEFAULT 'agent'::text NOT NULL,
    skill_id uuid,
    skill_version integer,
    CONSTRAINT eval_suite_runs_owner_consistency_chk CHECK ((((owner_kind = 'agent'::text) AND (skill_id IS NULL)) OR ((owner_kind = 'skill'::text) AND (skill_id IS NOT NULL)))),
    CONSTRAINT eval_suite_runs_owner_kind_chk CHECK ((owner_kind = ANY (ARRAY['skill'::text, 'agent'::text]))),
    CONSTRAINT eval_suite_runs_status_chk CHECK ((status = ANY (ARRAY['running'::text, 'completed'::text, 'failed'::text])))
);

--
-- Name: file_edges; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.file_edges (
    repo_id uuid NOT NULL,
    from_file text NOT NULL,
    to_file text NOT NULL
);

--
-- Name: file_facts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.file_facts (
    repo_id uuid NOT NULL,
    file_path text NOT NULL,
    endpoints jsonb DEFAULT '[]'::jsonb NOT NULL,
    crons jsonb DEFAULT '[]'::jsonb NOT NULL
);

--
-- Name: file_rank; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.file_rank (
    repo_id uuid NOT NULL,
    file_path text NOT NULL,
    pagerank double precision NOT NULL,
    hotness double precision NOT NULL,
    rank double precision NOT NULL,
    percentile smallint NOT NULL
);

--
-- Name: findings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.findings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    review_id uuid NOT NULL,
    file text NOT NULL,
    start_line integer NOT NULL,
    end_line integer NOT NULL,
    severity text NOT NULL,
    category text NOT NULL,
    title text NOT NULL,
    rationale text NOT NULL,
    suggestion text,
    confidence double precision NOT NULL,
    kind text DEFAULT 'finding'::text NOT NULL,
    trifecta_components jsonb,
    accepted_at timestamp with time zone,
    dismissed_at timestamp with time zone,
    skill_id uuid,
    skill_name text,
    out_of_scope boolean DEFAULT false NOT NULL,
    CONSTRAINT findings_category_chk CHECK ((category = ANY (ARRAY['bug'::text, 'security'::text, 'perf'::text, 'style'::text, 'test'::text]))),
    CONSTRAINT findings_kind_chk CHECK ((kind = ANY (ARRAY['finding'::text, 'secret_leak'::text, 'lethal_trifecta'::text, 'phantom'::text, 'hook'::text]))),
    CONSTRAINT findings_severity_chk CHECK ((severity = ANY (ARRAY['CRITICAL'::text, 'WARNING'::text, 'SUGGESTION'::text])))
);

--
-- Name: installed_plugins; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.installed_plugins (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    version text,
    source text,
    installed_at timestamp with time zone DEFAULT now() NOT NULL,
    enabled boolean DEFAULT true NOT NULL
);

--
-- Name: jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    kind text NOT NULL,
    payload jsonb,
    status text DEFAULT 'queued'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    scheduled_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    error text,
    CONSTRAINT jobs_status_chk CHECK ((status = ANY (ARRAY['queued'::text, 'running'::text, 'done'::text, 'failed'::text])))
);

--
-- Name: memory; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.memory (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    repo_id uuid,
    scope text NOT NULL,
    kind text NOT NULL,
    content text NOT NULL,
    embedding public.vector(1536),
    confidence double precision,
    sources jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    CONSTRAINT memory_kind_chk CHECK ((kind = ANY (ARRAY['decision'::text, 'convention'::text, 'preference'::text, 'fact'::text, 'learning'::text]))),
    CONSTRAINT memory_scope_chk CHECK ((scope = ANY (ARRAY['repo'::text, 'global'::text, 'team'::text])))
);

--
-- Name: multi_agent_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.multi_agent_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    pr_id uuid NOT NULL,
    ran_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: onboarding; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.onboarding (
    repo_id uuid NOT NULL,
    json jsonb NOT NULL,
    generated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: pr_brief; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pr_brief (
    pr_id uuid NOT NULL,
    json jsonb NOT NULL
);

--
-- Name: pr_commits; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pr_commits (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    pr_id uuid NOT NULL,
    sha text NOT NULL,
    message text NOT NULL,
    author text NOT NULL,
    committed_at timestamp with time zone
);

--
-- Name: pr_files; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pr_files (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    pr_id uuid NOT NULL,
    path text NOT NULL,
    additions integer DEFAULT 0 NOT NULL,
    deletions integer DEFAULT 0 NOT NULL,
    patch text
);

--
-- Name: pr_intent; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pr_intent (
    pr_id uuid NOT NULL,
    intent text NOT NULL,
    in_scope jsonb DEFAULT '[]'::jsonb NOT NULL,
    out_of_scope jsonb DEFAULT '[]'::jsonb NOT NULL,
    change_type text,
    confidence text,
    derived_from text DEFAULT 'inferred'::text NOT NULL,
    sources jsonb DEFAULT '[]'::jsonb NOT NULL,
    head_sha text,
    input_hash text,
    prompt_version integer,
    provider text,
    model text,
    tokens_in integer,
    tokens_out integer,
    cost_usd numeric(12,6),
    derived_at timestamp with time zone,
    CONSTRAINT pr_intent_change_type_chk CHECK ((change_type = ANY (ARRAY['feature'::text, 'bugfix'::text, 'refactor'::text, 'docs'::text, 'test'::text, 'chore'::text, 'security'::text, 'perf'::text, 'mixed'::text]))),
    CONSTRAINT pr_intent_confidence_chk CHECK ((confidence = ANY (ARRAY['high'::text, 'medium'::text, 'low'::text]))),
    CONSTRAINT pr_intent_derived_from_chk CHECK ((derived_from = ANY (ARRAY['explicit'::text, 'inferred'::text])))
);

--
-- Name: pull_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pull_requests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    number integer NOT NULL,
    title text NOT NULL,
    author text NOT NULL,
    branch text NOT NULL,
    base text NOT NULL,
    head_sha text NOT NULL,
    last_reviewed_sha text,
    additions integer DEFAULT 0 NOT NULL,
    deletions integer DEFAULT 0 NOT NULL,
    files_count integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'needs_review'::text NOT NULL,
    body text,
    opened_at timestamp with time zone,
    updated_at timestamp with time zone,
    CONSTRAINT pull_requests_status_chk CHECK ((status = ANY (ARRAY['needs_review'::text, 'reviewed'::text, 'stale'::text, 'open'::text, 'closed'::text, 'merged'::text])))
);

--
-- Name: references; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public."references" (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repo_id uuid NOT NULL,
    from_path text NOT NULL,
    to_symbol text NOT NULL,
    line integer NOT NULL,
    decl_file text,
    content_hash text
);

--
-- Name: repo_index_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repo_index_state (
    repo_id uuid NOT NULL,
    last_indexed_sha text NOT NULL,
    indexer_version integer NOT NULL,
    status text NOT NULL,
    files_indexed integer DEFAULT 0 NOT NULL,
    files_skipped integer DEFAULT 0 NOT NULL,
    stats jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT repo_index_state_status_chk CHECK ((status = ANY (ARRAY['full'::text, 'partial'::text, 'degraded'::text, 'failed'::text])))
);

--
-- Name: repo_map_cache; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repo_map_cache (
    repo_id uuid NOT NULL,
    commit_sha text NOT NULL,
    token_budget integer NOT NULL,
    map_text text NOT NULL,
    token_count integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: repos; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repos (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    owner text NOT NULL,
    name text NOT NULL,
    full_name text NOT NULL,
    default_branch text DEFAULT 'main'::text NOT NULL,
    clone_path text,
    last_polled_at timestamp with time zone,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: reviews; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.reviews (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    pr_id uuid NOT NULL,
    agent_id uuid,
    kind text NOT NULL,
    verdict text,
    summary text,
    score integer,
    model text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    run_id uuid,
    CONSTRAINT reviews_kind_chk CHECK ((kind = ANY (ARRAY['summary'::text, 'review'::text])))
);

--
-- Name: run_traces; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.run_traces (
    run_id uuid NOT NULL,
    trace jsonb NOT NULL
);

--
-- Name: settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.settings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    user_id uuid,
    key text NOT NULL,
    value jsonb
);

--
-- Name: skill_context_docs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.skill_context_docs (
    skill_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    path text NOT NULL,
    "position" integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT skill_context_docs_path_len_chk CHECK (((length(path) >= 1) AND (length(path) <= 512)))
);

--
-- Name: skill_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.skill_versions (
    skill_id uuid NOT NULL,
    version integer NOT NULL,
    body text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    description text,
    message text
);

--
-- Name: skills; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.skills (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    type text NOT NULL,
    source text NOT NULL,
    body text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    evidence_files jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    source_ref text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT skills_name_len_chk CHECK (((length(name) >= 1) AND (length(name) <= 64))),
    CONSTRAINT skills_source_chk CHECK ((source = ANY (ARRAY['manual'::text, 'imported_file'::text, 'imported_url'::text, 'extracted'::text, 'community'::text]))),
    CONSTRAINT skills_type_chk CHECK ((type = ANY (ARRAY['rubric'::text, 'convention'::text, 'security'::text, 'custom'::text])))
);

--
-- Name: symbols; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.symbols (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repo_id uuid NOT NULL,
    path text NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    line integer,
    end_line integer,
    exported boolean DEFAULT false NOT NULL,
    signature text,
    content_hash text
);

--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: workspace_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.workspace_members (
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text DEFAULT 'member'::text NOT NULL,
    CONSTRAINT workspace_members_role_chk CHECK ((role = ANY (ARRAY['owner'::text, 'member'::text])))
);

--
-- Name: workspaces; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.workspaces (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: agent_context_docs agent_context_docs_agent_id_repo_id_path_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_context_docs
    ADD CONSTRAINT agent_context_docs_agent_id_repo_id_path_pk PRIMARY KEY (agent_id, repo_id, path);

--
-- Name: agent_run_skills agent_run_skills_run_id_skill_id_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_skills
    ADD CONSTRAINT agent_run_skills_run_id_skill_id_pk PRIMARY KEY (run_id, skill_id);

--
-- Name: agent_runs agent_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_runs
    ADD CONSTRAINT agent_runs_pkey PRIMARY KEY (id);

--
-- Name: agent_skills agent_skills_agent_id_skill_id_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_skills
    ADD CONSTRAINT agent_skills_agent_id_skill_id_pk PRIMARY KEY (agent_id, skill_id);

--
-- Name: agent_versions agent_versions_agent_id_version_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_versions
    ADD CONSTRAINT agent_versions_agent_id_version_pk PRIMARY KEY (agent_id, version);

--
-- Name: agents agents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_pkey PRIMARY KEY (id);

--
-- Name: ci_installations ci_installations_agent_repo_target_uq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ci_installations
    ADD CONSTRAINT ci_installations_agent_repo_target_uq UNIQUE (agent_id, repo, target_type);

--
-- Name: ci_installations ci_installations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ci_installations
    ADD CONSTRAINT ci_installations_pkey PRIMARY KEY (id);

--
-- Name: ci_installations ci_installations_repo_slug_uq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ci_installations
    ADD CONSTRAINT ci_installations_repo_slug_uq UNIQUE (repo, slug);

--
-- Name: ci_runs ci_runs_github_run_attempt_uq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ci_runs
    ADD CONSTRAINT ci_runs_github_run_attempt_uq UNIQUE (github_run_id, run_attempt);

--
-- Name: ci_runs ci_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ci_runs
    ADD CONSTRAINT ci_runs_pkey PRIMARY KEY (id);

--
-- Name: code_chunks code_chunks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.code_chunks
    ADD CONSTRAINT code_chunks_pkey PRIMARY KEY (id);

--
-- Name: composed_reviews composed_reviews_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.composed_reviews
    ADD CONSTRAINT composed_reviews_pkey PRIMARY KEY (id);

--
-- Name: conformance_checks conformance_checks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conformance_checks
    ADD CONSTRAINT conformance_checks_pkey PRIMARY KEY (id);

--
-- Name: context_files context_files_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.context_files
    ADD CONSTRAINT context_files_pkey PRIMARY KEY (id);

--
-- Name: context_files context_files_repo_path_uq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.context_files
    ADD CONSTRAINT context_files_repo_path_uq UNIQUE (repo_id, path);

--
-- Name: convention_scans convention_scans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.convention_scans
    ADD CONSTRAINT convention_scans_pkey PRIMARY KEY (id);

--
-- Name: conventions conventions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conventions
    ADD CONSTRAINT conventions_pkey PRIMARY KEY (id);

--
-- Name: digests digests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.digests
    ADD CONSTRAINT digests_pkey PRIMARY KEY (id);

--
-- Name: eval_cases eval_cases_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.eval_cases
    ADD CONSTRAINT eval_cases_pkey PRIMARY KEY (id);

--
-- Name: eval_runs eval_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.eval_runs
    ADD CONSTRAINT eval_runs_pkey PRIMARY KEY (id);

--
-- Name: eval_suite_runs eval_suite_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.eval_suite_runs
    ADD CONSTRAINT eval_suite_runs_pkey PRIMARY KEY (id);

--
-- Name: file_edges file_edges_repo_id_from_file_to_file_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.file_edges
    ADD CONSTRAINT file_edges_repo_id_from_file_to_file_pk PRIMARY KEY (repo_id, from_file, to_file);

--
-- Name: file_facts file_facts_repo_id_file_path_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.file_facts
    ADD CONSTRAINT file_facts_repo_id_file_path_pk PRIMARY KEY (repo_id, file_path);

--
-- Name: file_rank file_rank_repo_id_file_path_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.file_rank
    ADD CONSTRAINT file_rank_repo_id_file_path_pk PRIMARY KEY (repo_id, file_path);

--
-- Name: findings findings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.findings
    ADD CONSTRAINT findings_pkey PRIMARY KEY (id);

--
-- Name: installed_plugins installed_plugins_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.installed_plugins
    ADD CONSTRAINT installed_plugins_pkey PRIMARY KEY (id);

--
-- Name: jobs jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_pkey PRIMARY KEY (id);

--
-- Name: memory memory_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory
    ADD CONSTRAINT memory_pkey PRIMARY KEY (id);

--
-- Name: multi_agent_runs multi_agent_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.multi_agent_runs
    ADD CONSTRAINT multi_agent_runs_pkey PRIMARY KEY (id);

--
-- Name: onboarding onboarding_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.onboarding
    ADD CONSTRAINT onboarding_pkey PRIMARY KEY (repo_id);

--
-- Name: pr_brief pr_brief_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pr_brief
    ADD CONSTRAINT pr_brief_pkey PRIMARY KEY (pr_id);

--
-- Name: pr_commits pr_commits_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pr_commits
    ADD CONSTRAINT pr_commits_pkey PRIMARY KEY (id);

--
-- Name: pr_commits pr_commits_pr_sha_uq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pr_commits
    ADD CONSTRAINT pr_commits_pr_sha_uq UNIQUE (pr_id, sha);

--
-- Name: pr_files pr_files_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pr_files
    ADD CONSTRAINT pr_files_pkey PRIMARY KEY (id);

--
-- Name: pr_files pr_files_pr_path_uq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pr_files
    ADD CONSTRAINT pr_files_pr_path_uq UNIQUE (pr_id, path);

--
-- Name: pr_intent pr_intent_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pr_intent
    ADD CONSTRAINT pr_intent_pkey PRIMARY KEY (pr_id);

--
-- Name: pull_requests pull_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pull_requests
    ADD CONSTRAINT pull_requests_pkey PRIMARY KEY (id);

--
-- Name: references references_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."references"
    ADD CONSTRAINT references_pkey PRIMARY KEY (id);

--
-- Name: repo_index_state repo_index_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repo_index_state
    ADD CONSTRAINT repo_index_state_pkey PRIMARY KEY (repo_id);

--
-- Name: repo_map_cache repo_map_cache_repo_id_commit_sha_token_budget_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repo_map_cache
    ADD CONSTRAINT repo_map_cache_repo_id_commit_sha_token_budget_pk PRIMARY KEY (repo_id, commit_sha, token_budget);

--
-- Name: repos repos_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repos
    ADD CONSTRAINT repos_pkey PRIMARY KEY (id);

--
-- Name: reviews reviews_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.reviews
    ADD CONSTRAINT reviews_pkey PRIMARY KEY (id);

--
-- Name: run_traces run_traces_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.run_traces
    ADD CONSTRAINT run_traces_pkey PRIMARY KEY (run_id);

--
-- Name: settings settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings
    ADD CONSTRAINT settings_pkey PRIMARY KEY (id);

--
-- Name: settings settings_ws_user_key_uq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings
    ADD CONSTRAINT settings_ws_user_key_uq UNIQUE NULLS NOT DISTINCT (workspace_id, user_id, key);

--
-- Name: skill_context_docs skill_context_docs_skill_id_repo_id_path_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.skill_context_docs
    ADD CONSTRAINT skill_context_docs_skill_id_repo_id_path_pk PRIMARY KEY (skill_id, repo_id, path);

--
-- Name: skill_versions skill_versions_skill_id_version_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.skill_versions
    ADD CONSTRAINT skill_versions_skill_id_version_pk PRIMARY KEY (skill_id, version);

--
-- Name: skills skills_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.skills
    ADD CONSTRAINT skills_pkey PRIMARY KEY (id);

--
-- Name: skills skills_ws_name_uq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.skills
    ADD CONSTRAINT skills_ws_name_uq UNIQUE (workspace_id, name);

--
-- Name: symbols symbols_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.symbols
    ADD CONSTRAINT symbols_pkey PRIMARY KEY (id);

--
-- Name: symbols symbols_repo_path_name_kind_line_uq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.symbols
    ADD CONSTRAINT symbols_repo_path_name_kind_line_uq UNIQUE NULLS NOT DISTINCT (repo_id, path, name, kind, line);

--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

--
-- Name: workspace_members workspace_members_workspace_id_user_id_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_members
    ADD CONSTRAINT workspace_members_workspace_id_user_id_pk PRIMARY KEY (workspace_id, user_id);

--
-- Name: workspaces workspaces_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspaces
    ADD CONSTRAINT workspaces_pkey PRIMARY KEY (id);

--
-- Name: agent_context_docs_repo_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agent_context_docs_repo_idx ON public.agent_context_docs USING btree (repo_id);

--
-- Name: agent_run_skills_skill_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agent_run_skills_skill_idx ON public.agent_run_skills USING btree (skill_id);

--
-- Name: agent_runs_multi_agent_run_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agent_runs_multi_agent_run_idx ON public.agent_runs USING btree (multi_agent_run_id);

--
-- Name: agent_runs_pr_ran_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agent_runs_pr_ran_idx ON public.agent_runs USING btree (pr_id, ran_at);

--
-- Name: agent_runs_running_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agent_runs_running_idx ON public.agent_runs USING btree (pr_id) WHERE (status = 'running'::text);

--
-- Name: agent_skills_skill_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agent_skills_skill_idx ON public.agent_skills USING btree (skill_id);

--
-- Name: agents_ws_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agents_ws_idx ON public.agents USING btree (workspace_id);

--
-- Name: code_chunks_embedding_hnsw_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX code_chunks_embedding_hnsw_idx ON public.code_chunks USING hnsw (embedding public.vector_cosine_ops);

--
-- Name: code_chunks_repo_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX code_chunks_repo_idx ON public.code_chunks USING btree (repo_id);

--
-- Name: convention_scans_one_running_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX convention_scans_one_running_uq ON public.convention_scans USING btree (repo_id) WHERE (status = 'running'::text);

--
-- Name: convention_scans_repo_started_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX convention_scans_repo_started_idx ON public.convention_scans USING btree (repo_id, started_at DESC NULLS LAST);

--
-- Name: conventions_repo_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX conventions_repo_idx ON public.conventions USING btree (repo_id);

--
-- Name: eval_cases_owner_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX eval_cases_owner_created_idx ON public.eval_cases USING btree (owner_kind, owner_id, created_at);

--
-- Name: eval_cases_source_finding_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX eval_cases_source_finding_uq ON public.eval_cases USING btree (source_finding_id);

--
-- Name: eval_runs_case_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX eval_runs_case_idx ON public.eval_runs USING btree (case_id);

--
-- Name: eval_runs_run_case_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX eval_runs_run_case_uq ON public.eval_runs USING btree (run_id, case_id);

--
-- Name: eval_suite_runs_agent_started_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX eval_suite_runs_agent_started_idx ON public.eval_suite_runs USING btree (agent_id, started_at);

--
-- Name: eval_suite_runs_one_running_agent_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX eval_suite_runs_one_running_agent_uq ON public.eval_suite_runs USING btree (agent_id) WHERE ((status = 'running'::text) AND (owner_kind = 'agent'::text));

--
-- Name: eval_suite_runs_one_running_skill_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX eval_suite_runs_one_running_skill_uq ON public.eval_suite_runs USING btree (skill_id) WHERE ((status = 'running'::text) AND (owner_kind = 'skill'::text));

--
-- Name: eval_suite_runs_skill_started_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX eval_suite_runs_skill_started_idx ON public.eval_suite_runs USING btree (skill_id, started_at);

--
-- Name: eval_suite_runs_ws_started_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX eval_suite_runs_ws_started_idx ON public.eval_suite_runs USING btree (workspace_id, started_at);

--
-- Name: file_edges_repo_to_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX file_edges_repo_to_idx ON public.file_edges USING btree (repo_id, to_file);

--
-- Name: file_rank_repo_rank_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX file_rank_repo_rank_idx ON public.file_rank USING btree (repo_id, rank);

--
-- Name: findings_review_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX findings_review_idx ON public.findings USING btree (review_id);

--
-- Name: findings_skill_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX findings_skill_idx ON public.findings USING btree (skill_id);

--
-- Name: jobs_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX jobs_status_idx ON public.jobs USING btree (status);

--
-- Name: jobs_ws_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX jobs_ws_idx ON public.jobs USING btree (workspace_id);

--
-- Name: memory_embedding_hnsw_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX memory_embedding_hnsw_idx ON public.memory USING hnsw (embedding public.vector_cosine_ops);

--
-- Name: memory_repo_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX memory_repo_idx ON public.memory USING btree (repo_id);

--
-- Name: memory_ws_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX memory_ws_idx ON public.memory USING btree (workspace_id);

--
-- Name: pr_repo_number_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX pr_repo_number_uq ON public.pull_requests USING btree (repo_id, number);

--
-- Name: pr_ws_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX pr_ws_idx ON public.pull_requests USING btree (workspace_id);

--
-- Name: references_repo_decl_symbol_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX references_repo_decl_symbol_idx ON public."references" USING btree (repo_id, decl_file, to_symbol);

--
-- Name: references_repo_from_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX references_repo_from_idx ON public."references" USING btree (repo_id, from_path);

--
-- Name: repos_ws_fullname_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX repos_ws_fullname_uq ON public.repos USING btree (workspace_id, full_name);

--
-- Name: repos_ws_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX repos_ws_idx ON public.repos USING btree (workspace_id);

--
-- Name: reviews_agent_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX reviews_agent_idx ON public.reviews USING btree (agent_id);

--
-- Name: reviews_pr_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX reviews_pr_created_idx ON public.reviews USING btree (pr_id, created_at);

--
-- Name: reviews_run_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX reviews_run_idx ON public.reviews USING btree (run_id);

--
-- Name: skill_context_docs_repo_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX skill_context_docs_repo_idx ON public.skill_context_docs USING btree (repo_id);

--
-- Name: symbols_repo_name_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX symbols_repo_name_idx ON public.symbols USING btree (repo_id, name);

--
-- Name: symbols_repo_path_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX symbols_repo_path_idx ON public.symbols USING btree (repo_id, path);

--
-- Name: agent_context_docs agent_context_docs_agent_id_agents_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_context_docs
    ADD CONSTRAINT agent_context_docs_agent_id_agents_id_fk FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;

--
-- Name: agent_context_docs agent_context_docs_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_context_docs
    ADD CONSTRAINT agent_context_docs_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: agent_run_skills agent_run_skills_run_id_agent_runs_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_skills
    ADD CONSTRAINT agent_run_skills_run_id_agent_runs_id_fk FOREIGN KEY (run_id) REFERENCES public.agent_runs(id) ON DELETE CASCADE;

--
-- Name: agent_run_skills agent_run_skills_skill_id_skills_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_skills
    ADD CONSTRAINT agent_run_skills_skill_id_skills_id_fk FOREIGN KEY (skill_id) REFERENCES public.skills(id) ON DELETE CASCADE;

--
-- Name: agent_runs agent_runs_agent_id_agents_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_runs
    ADD CONSTRAINT agent_runs_agent_id_agents_id_fk FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE SET NULL;

--
-- Name: agent_runs agent_runs_multi_agent_run_id_multi_agent_runs_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_runs
    ADD CONSTRAINT agent_runs_multi_agent_run_id_multi_agent_runs_id_fk FOREIGN KEY (multi_agent_run_id) REFERENCES public.multi_agent_runs(id) ON DELETE SET NULL;

--
-- Name: agent_runs agent_runs_pr_id_pull_requests_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_runs
    ADD CONSTRAINT agent_runs_pr_id_pull_requests_id_fk FOREIGN KEY (pr_id) REFERENCES public.pull_requests(id) ON DELETE SET NULL;

--
-- Name: agent_runs agent_runs_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_runs
    ADD CONSTRAINT agent_runs_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: agent_skills agent_skills_agent_id_agents_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_skills
    ADD CONSTRAINT agent_skills_agent_id_agents_id_fk FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;

--
-- Name: agent_skills agent_skills_skill_id_skills_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_skills
    ADD CONSTRAINT agent_skills_skill_id_skills_id_fk FOREIGN KEY (skill_id) REFERENCES public.skills(id) ON DELETE CASCADE;

--
-- Name: agent_versions agent_versions_agent_id_agents_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_versions
    ADD CONSTRAINT agent_versions_agent_id_agents_id_fk FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;

--
-- Name: agents agents_created_by_users_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_created_by_users_id_fk FOREIGN KEY (created_by) REFERENCES public.users(id);

--
-- Name: agents agents_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: ci_installations ci_installations_agent_id_agents_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ci_installations
    ADD CONSTRAINT ci_installations_agent_id_agents_id_fk FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;

--
-- Name: ci_runs ci_runs_agent_run_id_agent_runs_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ci_runs
    ADD CONSTRAINT ci_runs_agent_run_id_agent_runs_id_fk FOREIGN KEY (agent_run_id) REFERENCES public.agent_runs(id) ON DELETE SET NULL;

--
-- Name: ci_runs ci_runs_ci_installation_id_ci_installations_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ci_runs
    ADD CONSTRAINT ci_runs_ci_installation_id_ci_installations_id_fk FOREIGN KEY (ci_installation_id) REFERENCES public.ci_installations(id) ON DELETE SET NULL;

--
-- Name: code_chunks code_chunks_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.code_chunks
    ADD CONSTRAINT code_chunks_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: code_chunks code_chunks_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.code_chunks
    ADD CONSTRAINT code_chunks_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: composed_reviews composed_reviews_pr_id_pull_requests_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.composed_reviews
    ADD CONSTRAINT composed_reviews_pr_id_pull_requests_id_fk FOREIGN KEY (pr_id) REFERENCES public.pull_requests(id) ON DELETE CASCADE;

--
-- Name: conformance_checks conformance_checks_pr_id_pull_requests_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conformance_checks
    ADD CONSTRAINT conformance_checks_pr_id_pull_requests_id_fk FOREIGN KEY (pr_id) REFERENCES public.pull_requests(id) ON DELETE CASCADE;

--
-- Name: context_files context_files_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.context_files
    ADD CONSTRAINT context_files_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: convention_scans convention_scans_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.convention_scans
    ADD CONSTRAINT convention_scans_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: convention_scans convention_scans_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.convention_scans
    ADD CONSTRAINT convention_scans_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: conventions conventions_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conventions
    ADD CONSTRAINT conventions_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: conventions conventions_scan_id_convention_scans_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conventions
    ADD CONSTRAINT conventions_scan_id_convention_scans_id_fk FOREIGN KEY (scan_id) REFERENCES public.convention_scans(id) ON DELETE SET NULL;

--
-- Name: conventions conventions_skill_id_skills_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conventions
    ADD CONSTRAINT conventions_skill_id_skills_id_fk FOREIGN KEY (skill_id) REFERENCES public.skills(id) ON DELETE SET NULL;

--
-- Name: conventions conventions_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conventions
    ADD CONSTRAINT conventions_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: digests digests_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.digests
    ADD CONSTRAINT digests_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: eval_cases eval_cases_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.eval_cases
    ADD CONSTRAINT eval_cases_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: eval_runs eval_runs_case_id_eval_cases_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.eval_runs
    ADD CONSTRAINT eval_runs_case_id_eval_cases_id_fk FOREIGN KEY (case_id) REFERENCES public.eval_cases(id) ON DELETE CASCADE;

--
-- Name: eval_runs eval_runs_run_id_eval_suite_runs_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.eval_runs
    ADD CONSTRAINT eval_runs_run_id_eval_suite_runs_id_fk FOREIGN KEY (run_id) REFERENCES public.eval_suite_runs(id) ON DELETE CASCADE;

--
-- Name: eval_suite_runs eval_suite_runs_agent_id_agents_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.eval_suite_runs
    ADD CONSTRAINT eval_suite_runs_agent_id_agents_id_fk FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;

--
-- Name: eval_suite_runs eval_suite_runs_skill_id_skills_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.eval_suite_runs
    ADD CONSTRAINT eval_suite_runs_skill_id_skills_id_fk FOREIGN KEY (skill_id) REFERENCES public.skills(id) ON DELETE CASCADE;

--
-- Name: eval_suite_runs eval_suite_runs_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.eval_suite_runs
    ADD CONSTRAINT eval_suite_runs_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: file_edges file_edges_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.file_edges
    ADD CONSTRAINT file_edges_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: file_facts file_facts_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.file_facts
    ADD CONSTRAINT file_facts_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: file_rank file_rank_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.file_rank
    ADD CONSTRAINT file_rank_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: findings findings_review_id_reviews_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.findings
    ADD CONSTRAINT findings_review_id_reviews_id_fk FOREIGN KEY (review_id) REFERENCES public.reviews(id) ON DELETE CASCADE;

--
-- Name: findings findings_skill_id_skills_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.findings
    ADD CONSTRAINT findings_skill_id_skills_id_fk FOREIGN KEY (skill_id) REFERENCES public.skills(id) ON DELETE SET NULL;

--
-- Name: installed_plugins installed_plugins_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.installed_plugins
    ADD CONSTRAINT installed_plugins_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: jobs jobs_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: memory memory_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory
    ADD CONSTRAINT memory_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: memory memory_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.memory
    ADD CONSTRAINT memory_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: multi_agent_runs multi_agent_runs_pr_id_pull_requests_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.multi_agent_runs
    ADD CONSTRAINT multi_agent_runs_pr_id_pull_requests_id_fk FOREIGN KEY (pr_id) REFERENCES public.pull_requests(id) ON DELETE CASCADE;

--
-- Name: multi_agent_runs multi_agent_runs_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.multi_agent_runs
    ADD CONSTRAINT multi_agent_runs_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: onboarding onboarding_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.onboarding
    ADD CONSTRAINT onboarding_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: pr_brief pr_brief_pr_id_pull_requests_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pr_brief
    ADD CONSTRAINT pr_brief_pr_id_pull_requests_id_fk FOREIGN KEY (pr_id) REFERENCES public.pull_requests(id) ON DELETE CASCADE;

--
-- Name: pr_commits pr_commits_pr_id_pull_requests_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pr_commits
    ADD CONSTRAINT pr_commits_pr_id_pull_requests_id_fk FOREIGN KEY (pr_id) REFERENCES public.pull_requests(id) ON DELETE CASCADE;

--
-- Name: pr_files pr_files_pr_id_pull_requests_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pr_files
    ADD CONSTRAINT pr_files_pr_id_pull_requests_id_fk FOREIGN KEY (pr_id) REFERENCES public.pull_requests(id) ON DELETE CASCADE;

--
-- Name: pr_intent pr_intent_pr_id_pull_requests_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pr_intent
    ADD CONSTRAINT pr_intent_pr_id_pull_requests_id_fk FOREIGN KEY (pr_id) REFERENCES public.pull_requests(id) ON DELETE CASCADE;

--
-- Name: pull_requests pull_requests_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pull_requests
    ADD CONSTRAINT pull_requests_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: pull_requests pull_requests_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pull_requests
    ADD CONSTRAINT pull_requests_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: references references_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public."references"
    ADD CONSTRAINT references_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: repo_index_state repo_index_state_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repo_index_state
    ADD CONSTRAINT repo_index_state_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: repo_map_cache repo_map_cache_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repo_map_cache
    ADD CONSTRAINT repo_map_cache_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: repos repos_created_by_users_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repos
    ADD CONSTRAINT repos_created_by_users_id_fk FOREIGN KEY (created_by) REFERENCES public.users(id);

--
-- Name: repos repos_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repos
    ADD CONSTRAINT repos_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: reviews reviews_agent_id_agents_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.reviews
    ADD CONSTRAINT reviews_agent_id_agents_id_fk FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE SET NULL;

--
-- Name: reviews reviews_pr_id_pull_requests_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.reviews
    ADD CONSTRAINT reviews_pr_id_pull_requests_id_fk FOREIGN KEY (pr_id) REFERENCES public.pull_requests(id) ON DELETE CASCADE;

--
-- Name: reviews reviews_run_id_agent_runs_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.reviews
    ADD CONSTRAINT reviews_run_id_agent_runs_id_fk FOREIGN KEY (run_id) REFERENCES public.agent_runs(id) ON DELETE CASCADE;

--
-- Name: reviews reviews_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.reviews
    ADD CONSTRAINT reviews_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: run_traces run_traces_run_id_agent_runs_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.run_traces
    ADD CONSTRAINT run_traces_run_id_agent_runs_id_fk FOREIGN KEY (run_id) REFERENCES public.agent_runs(id) ON DELETE CASCADE;

--
-- Name: settings settings_user_id_users_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings
    ADD CONSTRAINT settings_user_id_users_id_fk FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: settings settings_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings
    ADD CONSTRAINT settings_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: skill_context_docs skill_context_docs_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.skill_context_docs
    ADD CONSTRAINT skill_context_docs_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: skill_context_docs skill_context_docs_skill_id_skills_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.skill_context_docs
    ADD CONSTRAINT skill_context_docs_skill_id_skills_id_fk FOREIGN KEY (skill_id) REFERENCES public.skills(id) ON DELETE CASCADE;

--
-- Name: skill_versions skill_versions_skill_id_skills_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.skill_versions
    ADD CONSTRAINT skill_versions_skill_id_skills_id_fk FOREIGN KEY (skill_id) REFERENCES public.skills(id) ON DELETE CASCADE;

--
-- Name: skills skills_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.skills
    ADD CONSTRAINT skills_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;

--
-- Name: symbols symbols_repo_id_repos_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.symbols
    ADD CONSTRAINT symbols_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE;

--
-- Name: workspace_members workspace_members_user_id_users_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_members
    ADD CONSTRAINT workspace_members_user_id_users_id_fk FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: workspace_members workspace_members_workspace_id_workspaces_id_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_members
    ADD CONSTRAINT workspace_members_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE;
