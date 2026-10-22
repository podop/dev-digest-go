/** smartdiff.json — classifyFile, buildSmartDiff and the service over emulated tables (AC5043, AC5006, EC5013). */
import { corpusJson, expandText, showError, type Obj } from './common.mts';
import type { Ctx } from './ctx.mts';

export async function genSmartDiff(ctx: Ctx): Promise<void> {
  const { N } = ctx;
  const classifySpec = expandText(corpusJson(ctx, 'smartdiff/classify.json'));
  const classify = (classifySpec.paths as string[]).map((path) => ({ path, role: N.smartDiffClassify.classifyFile(path) as string }));

  const buildSpec = expandText(corpusJson(ctx, 'smartdiff/build.json'));
  const build = (buildSpec.cases as Obj[]).map((c) => ({
    name: c.name,
    files: c.files,
    findings: c.findings,
    result: N.smartDiffBuild.buildSmartDiff(c.files, c.findings),
  }));

  const serviceSpec = expandText(corpusJson(ctx, 'smartdiff/service.json'));
  const workspace = serviceSpec.workspace as string;
  const service: Obj[] = [];
  for (const c of serviceSpec.cases as Obj[]) {
    const source = {
      pullExists: async (ws: string, prId: string) => ws === workspace && c.pr !== null && c.pr.id === prId,
      listFiles: async () => (c.files as Obj[]).map((f) => ({ path: f.path, additions: f.additions, deletions: f.deletions })),
      // Emulates SelectDistinctOn(agent_id) ... ORDER BY agent_id, created_at DESC, then the findings of those reviews.
      latestReviewFindings: async () => {
        const reviews = (c.reviews as Obj[]).filter((r) => r.kind === 'review');
        const newest = new Map<string | null, Obj>();
        for (const r of reviews) {
          const key = (r.agent_id as string | null) ?? null;
          const cur = newest.get(key);
          if (!cur || Date.parse(r.created_at) > Date.parse(cur.created_at)) newest.set(key, r);
        }
        const ids = new Set([...newest.values()].map((r) => r.id as string));
        return (c.findings as Obj[]).filter((f) => ids.has(f.review_id)).map((f) => ({ file: f.file, start_line: f.start_line }));
      },
    };
    const svc = new N.smartDiffService.SmartDiffService({ source });
    const askFor = (c.askFor as string | undefined) ?? c.pr?.id;
    let outcome: Obj;
    try {
      outcome = { status: 200, body: await svc.get(workspace, askFor) };
    } catch (e) {
      outcome = { error: showError(e) };
    }
    service.push({ name: c.name, workspace, askFor, tables: { pr: c.pr, files: c.files, reviews: c.reviews, findings: c.findings }, ...outcome });
  }

  ctx.writer.write('smartdiff.json', {
    note: 'classify: classifyFile(path) for every corpus path (paths are decoded from the corpus, so \\u escapes are real characters). build: buildSmartDiff(files, findings). service: SmartDiffService.get over fake tables; the "latest review per agent" step is SQL in Node and is emulated by the generator (see corpus/smartdiff/service.json), the rest is the Node code. A SmartDiff body never has a pseudocode_summary key.',
    classify,
    build,
    service,
  });
}
