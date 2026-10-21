CREATE TABLE "eval_suite_runs" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"workspace_id" uuid NOT NULL,
	"agent_id" uuid NOT NULL,
	"agent_version" integer NOT NULL,
	"config" jsonb NOT NULL,
	"case_ids" jsonb NOT NULL,
	"status" text DEFAULT 'running' NOT NULL,
	"started_at" timestamp with time zone DEFAULT now() NOT NULL,
	"finished_at" timestamp with time zone,
	"cases_total" integer DEFAULT 0 NOT NULL,
	"cases_finished" integer DEFAULT 0 NOT NULL,
	"cases_passed" integer DEFAULT 0 NOT NULL,
	"cases_errored" integer DEFAULT 0 NOT NULL,
	"recall" double precision,
	"precision" double precision,
	"citation_accuracy" double precision,
	"unlabeled_findings" integer DEFAULT 0 NOT NULL,
	"cost_usd" numeric(12, 6),
	"duration_ms" integer,
	"error" text,
	CONSTRAINT "eval_suite_runs_status_chk" CHECK ("eval_suite_runs"."status" IN ('running', 'completed', 'failed'))
);
--> statement-breakpoint
ALTER TABLE "eval_cases" ADD COLUMN "created_at" timestamp with time zone DEFAULT now() NOT NULL;--> statement-breakpoint
ALTER TABLE "eval_cases" ADD COLUMN "source_finding_id" uuid;--> statement-breakpoint
ALTER TABLE "eval_cases" ADD COLUMN "source_review_id" uuid;--> statement-breakpoint
ALTER TABLE "eval_cases" ADD COLUMN "source_pr_id" uuid;--> statement-breakpoint
ALTER TABLE "eval_runs" ADD COLUMN "run_id" uuid;--> statement-breakpoint
ALTER TABLE "eval_runs" ADD COLUMN "status" text;--> statement-breakpoint
ALTER TABLE "eval_runs" ADD COLUMN "error" text;--> statement-breakpoint
ALTER TABLE "eval_runs" ADD COLUMN "dropped_count" integer DEFAULT 0 NOT NULL;--> statement-breakpoint
ALTER TABLE "eval_runs" ADD COLUMN "hit_count" integer DEFAULT 0 NOT NULL;--> statement-breakpoint
ALTER TABLE "eval_runs" ADD COLUMN "violation_count" integer DEFAULT 0 NOT NULL;--> statement-breakpoint
ALTER TABLE "eval_runs" ADD COLUMN "unlabeled_count" integer DEFAULT 0 NOT NULL;--> statement-breakpoint
ALTER TABLE "eval_suite_runs" ADD CONSTRAINT "eval_suite_runs_workspace_id_workspaces_id_fk" FOREIGN KEY ("workspace_id") REFERENCES "public"."workspaces"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "eval_suite_runs" ADD CONSTRAINT "eval_suite_runs_agent_id_agents_id_fk" FOREIGN KEY ("agent_id") REFERENCES "public"."agents"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "eval_suite_runs_one_running_uq" ON "eval_suite_runs" USING btree ("agent_id") WHERE "eval_suite_runs"."status" = 'running';--> statement-breakpoint
CREATE INDEX "eval_suite_runs_agent_started_idx" ON "eval_suite_runs" USING btree ("agent_id","started_at");--> statement-breakpoint
CREATE INDEX "eval_suite_runs_ws_started_idx" ON "eval_suite_runs" USING btree ("workspace_id","started_at");--> statement-breakpoint
ALTER TABLE "eval_runs" ADD CONSTRAINT "eval_runs_run_id_eval_suite_runs_id_fk" FOREIGN KEY ("run_id") REFERENCES "public"."eval_suite_runs"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "eval_cases_source_finding_uq" ON "eval_cases" USING btree ("source_finding_id");--> statement-breakpoint
CREATE INDEX "eval_cases_owner_created_idx" ON "eval_cases" USING btree ("owner_kind","owner_id","created_at");--> statement-breakpoint
CREATE UNIQUE INDEX "eval_runs_run_case_uq" ON "eval_runs" USING btree ("run_id","case_id");--> statement-breakpoint
ALTER TABLE "eval_runs" ADD CONSTRAINT "eval_runs_status_chk" CHECK ("eval_runs"."status" IN ('passed', 'failed', 'error'));