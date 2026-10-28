-- Repo-intel Go/Java support (spec repo-intel-go-java FR18).
-- file_packages maps each indexed source file that belongs to a package (Go: the directory) to its
-- package key, so ResolveReferences can resolve a reference to a symbol declared in another file of
-- the same package. Files without a package key (TypeScript, JavaScript) have no row. Deleted with
-- the repo.
CREATE TABLE public.file_packages (
    repo_id uuid NOT NULL,
    file_path text NOT NULL,
    package_key text NOT NULL,
    CONSTRAINT file_packages_repo_id_file_path_pk PRIMARY KEY (repo_id, file_path),
    CONSTRAINT file_packages_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES public.repos(id) ON DELETE CASCADE
);

CREATE INDEX file_packages_repo_key_idx ON public.file_packages USING btree (repo_id, package_key);
