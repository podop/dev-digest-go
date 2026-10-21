DROP INDEX "eval_suite_runs_one_running_uq";--> statement-breakpoint
ALTER TABLE "eval_cases" ADD COLUMN "content_edited_at" timestamp with time zone;--> statement-breakpoint
ALTER TABLE "eval_suite_runs" ADD COLUMN "agent_name" text DEFAULT '' NOT NULL;--> statement-breakpoint
ALTER TABLE "eval_suite_runs" ADD COLUMN "owner_kind" text DEFAULT 'agent' NOT NULL;--> statement-breakpoint
ALTER TABLE "eval_suite_runs" ADD COLUMN "skill_id" uuid;--> statement-breakpoint
ALTER TABLE "eval_suite_runs" ADD COLUMN "skill_version" integer;--> statement-breakpoint
-- Hand-added backfill: existing runs are agent runs; freeze the current agent name on them.
UPDATE "eval_suite_runs" r SET "agent_name" = a."name" FROM "agents" a WHERE a."id" = r."agent_id";--> statement-breakpoint
ALTER TABLE "eval_suite_runs" ADD CONSTRAINT "eval_suite_runs_skill_id_skills_id_fk" FOREIGN KEY ("skill_id") REFERENCES "public"."skills"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "eval_suite_runs_one_running_agent_uq" ON "eval_suite_runs" USING btree ("agent_id") WHERE "eval_suite_runs"."status" = 'running' and "eval_suite_runs"."owner_kind" = 'agent';--> statement-breakpoint
CREATE UNIQUE INDEX "eval_suite_runs_one_running_skill_uq" ON "eval_suite_runs" USING btree ("skill_id") WHERE "eval_suite_runs"."status" = 'running' and "eval_suite_runs"."owner_kind" = 'skill';--> statement-breakpoint
CREATE INDEX "eval_suite_runs_skill_started_idx" ON "eval_suite_runs" USING btree ("skill_id","started_at");--> statement-breakpoint
ALTER TABLE "eval_suite_runs" ADD CONSTRAINT "eval_suite_runs_owner_kind_chk" CHECK ("eval_suite_runs"."owner_kind" IN ('skill', 'agent'));--> statement-breakpoint
ALTER TABLE "eval_suite_runs" ADD CONSTRAINT "eval_suite_runs_owner_consistency_chk" CHECK (("eval_suite_runs"."owner_kind" = 'agent' and "eval_suite_runs"."skill_id" is null) or ("eval_suite_runs"."owner_kind" = 'skill' and "eval_suite_runs"."skill_id" is not null));