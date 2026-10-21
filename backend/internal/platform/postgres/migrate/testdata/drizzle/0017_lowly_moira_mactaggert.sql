CREATE TABLE "context_files" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"repo_id" uuid NOT NULL,
	"path" text NOT NULL,
	"content" text NOT NULL,
	"size_bytes" integer NOT NULL,
	"version" integer DEFAULT 1 NOT NULL,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	"updated_at" timestamp with time zone DEFAULT now() NOT NULL,
	CONSTRAINT "context_files_repo_path_uq" UNIQUE("repo_id","path"),
	CONSTRAINT "context_files_path_len_chk" CHECK (length("context_files"."path") BETWEEN 1 AND 512)
);
--> statement-breakpoint
ALTER TABLE "context_files" ADD CONSTRAINT "context_files_repo_id_repos_id_fk" FOREIGN KEY ("repo_id") REFERENCES "public"."repos"("id") ON DELETE cascade ON UPDATE no action;