/**
 * git.json — SimpleGitClient.readFileAt over a throw-away repository: the size cap, the sha/path rules, the
 * decoding of git output (the doc source and the onboarding reader sit on top of it).
 */
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { corpusJson, expandText, showError, type Obj } from './common.mts';
import type { Ctx } from './ctx.mts';

const GIT_ENV = {
  GIT_AUTHOR_NAME: 'Parity',
  GIT_AUTHOR_EMAIL: 'parity@example.invalid',
  GIT_COMMITTER_NAME: 'Parity',
  GIT_COMMITTER_EMAIL: 'parity@example.invalid',
  GIT_AUTHOR_DATE: '2026-01-01T00:00:00Z',
  GIT_COMMITTER_DATE: '2026-01-01T00:00:00Z',
  GIT_CONFIG_NOSYSTEM: '1',
  GIT_CONFIG_GLOBAL: '/dev/null',
  GIT_TERMINAL_PROMPT: '0',
};

export async function genGit(ctx: Ctx): Promise<void> {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'git/cases.json'));
  const repo = spec.repo as Obj;
  const scratch = mkdtempSync(join(process.env.TMPDIR!, 'p5-git-'));
  const results: Obj[] = [];
  let commit = '';
  try {
    const cloneDir = join(scratch, 'clones');
    const root = join(cloneDir, repo.owner, repo.name);
    mkdirSync(root, { recursive: true });
    const git = (...args: string[]): string =>
      execFileSync('git', ['-c', 'core.autocrlf=false', '-c', 'core.symlinks=true', '-c', 'core.fsmonitor=false', ...args], {
        cwd: root,
        encoding: 'utf8',
        env: { PATH: process.env.PATH ?? '', HOME: scratch, ...GIT_ENV },
      });
    git('init', '-q', '-b', 'main');
    for (const f of spec.files as Obj[]) {
      const full = join(root, f.path);
      mkdirSync(dirname(full), { recursive: true });
      if (f.link !== undefined) symlinkSync(f.link, full);
      else if (f.hex !== undefined) writeFileSync(full, Buffer.from(f.hex as string, 'hex'));
      else if (f.fill !== undefined) writeFileSync(full, (f.fill as string).repeat((f.bytes as number) / Buffer.byteLength(f.fill as string)));
      else writeFileSync(full, f.text as string);
    }
    git('add', '-A');
    git('commit', '-q', '-m', 'fixture');
    commit = git('rev-parse', 'HEAD').trim();

    const client = new N.simpleGit.SimpleGitClient(cloneDir);
    for (const c of spec.cases as Obj[]) {
      let sha = (c.sha as string | undefined) ?? 'HEAD';
      if (sha === 'HEAD') sha = commit;
      else if (sha === 'HEAD:short7') sha = commit.slice(0, 7);
      else if (sha === 'HEAD:colon') sha = `${commit}:x`;
      else if (sha === 'HEAD:plus1') sha = `${commit}0`;
      let outcome: Obj;
      try {
        const r = await client.readFileAt(repo, sha, c.path, c.maxBytes === undefined ? undefined : { maxBytes: c.maxBytes });
        outcome = { ok: true, path: r.path, size: r.size, content: r.content };
      } catch (e) {
        outcome = { ok: false, error: showError(e) };
      }
      results.push({ name: c.name, path: c.path, sha: c.sha === undefined ? 'HEAD' : c.sha, effectiveSha: sha, maxBytes: c.maxBytes ?? null, ...outcome });
    }
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
  ctx.writer.write('git.json', {
    note: 'SimpleGitClient.readFileAt over a repository built from corpus/git/cases.json (fixed identity and dates: `commit` is reproducible). `sha` HEAD = commit; HEAD:short7 / HEAD:colon / HEAD:plus1 derive from it. Error messages contain the sha (replace `commit` by a placeholder in Go tests). content is what `git show` returned through simple-git (decoded as UTF-8 by Node).',
    commit,
    repo,
    files: spec.files,
    results,
  });
}
