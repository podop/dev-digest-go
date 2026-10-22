/** callers.json — extractReferences and the Node getCallerSignatures use case over fixture clones (NFR4005 "CallerSignatures reference rules"). */
import { cpSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { byteCompare, textOf, type TextSpec } from './lib.mts';
import { corpusJson, type Obj } from './common.mts';
import type { Ctx } from './ctx.mts';

export async function genCallers(ctx: Ctx): Promise<void> {
  const { N } = ctx;
  const references = (corpusJson(ctx, 'callers/references.json').cases as Obj[]).map((c) => {
    const content = textOf(c as TextSpec);
    return { name: c.name, symbol: c.symbol, content, lines: N.codeindexExtract.extractReferences(content, c.symbol).map((r: Obj) => r.line) };
  });

  const scratchBase = process.env.TMPDIR!;
  const scratch = mkdtempSync(join(scratchBase, 'p4-callers-'));
  const signatures: Obj[] = [];
  try {
    const spec = corpusJson(ctx, 'callers/cases.json');
    for (const c of spec.cases as Obj[]) {
      const root = join(scratch, c.name, 'repo');
      mkdirSync(root, { recursive: true });
      cpSync(join(ctx.corpus, 'callers', 'fixtures', c.fixture), root, { recursive: true });
      for (const f of (c.extraFiles ?? []) as Obj[]) {
        const full = join(root, f.path);
        mkdirSync(dirname(full), { recursive: true });
        if (f.link !== undefined) symlinkSync(f.link, full);
        else if (f.padTo !== undefined) {
          const head = f.content as string;
          writeFileSync(full, head + '//' + 'p'.repeat(f.padTo - head.length - 2));
        } else writeFileSync(full, f.content);
      }
      const real = new N.ripgrep.RipgrepCodeIndex({ clonePathFor: () => root });
      const deps = {
        enabled: c.enabled !== false,
        reader: {
          getRepoBasics: async () => ({ owner: 'acme', name: 'shop', clonePath: c.noClone ? null : root }),
          getFileRankFor: async (_id: string, paths: string[]) => ((c.ranks ?? []) as Obj[]).filter((r) => paths.includes(r.path)),
        },
        codeIndex: {
          references: async (repo: Obj, symbol: string) => {
            const refs: Obj[] = await real.references(repo, symbol);
            return refs.sort((a, b) => byteCompare(a.fromPath, b.fromPath) || a.line - b.line);
          },
        },
        analyzer: N.sourceAdapters.astGrepSourceAnalyzer,
        files: N.sourceAdapters.cloneFileSystem,
      };
      const rows = await N.sourceQueries.getCallerSignatures(deps, 'repo-1', c.changedFiles, c.limit ?? undefined);
      signatures.push({ ...c, rows });
    }
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }

  ctx.writer.write('callers.json', {
    note: 'references: extractReferences(content, symbol) → 1-based line numbers (the Node rules the callers scan uses). signatures: getCallerSignatures over the fixture clone (corpus/callers/fixtures/<fixture> + extraFiles), references sorted by path bytes then line; rows are {file, symbol, signature, rank}. Node reads changed files without a size cap and follows symlinks while reading them; the Go port (spec 7.11) caps reads at 409600 bytes and never follows symlinks, so those two cases are not in the corpus.',
    references,
    signatures,
  });
}
