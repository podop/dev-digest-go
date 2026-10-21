ALTER TABLE "ci_installations" ADD COLUMN "slug" text;--> statement-breakpoint
ALTER TABLE "ci_installations" ADD COLUMN "manifest_version" integer;--> statement-breakpoint
ALTER TABLE "ci_runs" ADD COLUMN "agent_run_id" uuid;--> statement-breakpoint
ALTER TABLE "ci_runs" ADD COLUMN "github_run_id" bigint;--> statement-breakpoint
ALTER TABLE "ci_runs" ADD COLUMN "run_attempt" integer;--> statement-breakpoint
ALTER TABLE "ci_runs" ADD COLUMN "head_sha" text;--> statement-breakpoint
ALTER TABLE "ci_runs" ADD COLUMN "manifest_version" integer;--> statement-breakpoint
ALTER TABLE "agent_runs" ADD COLUMN "multi_agent_run_id" uuid;--> statement-breakpoint
ALTER TABLE "ci_runs" ADD CONSTRAINT "ci_runs_agent_run_id_agent_runs_id_fk" FOREIGN KEY ("agent_run_id") REFERENCES "public"."agent_runs"("id") ON DELETE set null ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "agent_runs" ADD CONSTRAINT "agent_runs_multi_agent_run_id_multi_agent_runs_id_fk" FOREIGN KEY ("multi_agent_run_id") REFERENCES "public"."multi_agent_runs"("id") ON DELETE set null ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "agent_runs_multi_agent_run_idx" ON "agent_runs" USING btree ("multi_agent_run_id");--> statement-breakpoint
ALTER TABLE "ci_installations" ADD CONSTRAINT "ci_installations_agent_repo_target_uq" UNIQUE("agent_id","repo","target_type");--> statement-breakpoint
ALTER TABLE "ci_installations" ADD CONSTRAINT "ci_installations_repo_slug_uq" UNIQUE("repo","slug");--> statement-breakpoint
ALTER TABLE "ci_runs" ADD CONSTRAINT "ci_runs_github_run_attempt_uq" UNIQUE("github_run_id","run_attempt");