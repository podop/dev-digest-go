package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
)

// Paths of the AC10 fixture.
const (
	javaController = "src/main/java/com/acme/web/OrderController.java"
	javaMapper     = "src/main/java/com/acme/web/OrderMapper.java"
	javaService    = "src/main/java/com/acme/svc/OrderService.java"
	javaRepository = "src/main/java/com/acme/svc/OrderRepository.java"
	javaServiceTst = "src/test/java/com/acme/svc/OrderServiceTest.java"
	javaIds        = "src/main/java/com/acme/util/Ids.java"
	javaJob        = "src/main/java/com/acme/jobs/CleanupJob.java"
)

// javaOrderController is the AC10 controller: a class prefix, a nested static class, a constructor,
// a GET and a multi-line POST mapping that call the service, and a mapping with a non-literal path.
const javaOrderController = `package com.acme.web;

import com.acme.svc.OrderService;
import static com.acme.util.Ids.next;
import java.util.List;

@RestController
@RequestMapping("/api/orders")
public class OrderController {
    static final String BASE = "/v1";

    private final OrderService service;

    static class Page {
        int size;
    }

    public OrderController(OrderService service) {
        this.service = service;
    }

    @GetMapping("/{id}")
    public String get(@PathVariable String id) {
        return service.find(id);
    }

    @RequestMapping(value = "/search",
        method = RequestMethod.POST)
    public List<String> search(String q) {
        return service.search(q, next());
    }

    @GetMapping(BASE + "/x")
    String legacy() {
        return BASE;
    }
}
`

// javaOrderService is the AC10 service; countPending is package-private.
const javaOrderService = `package com.acme.svc;

import java.util.List;

public class OrderService {
    private final OrderRepository repo = new OrderRepository();

    public String find(String id) {
        return repo.load(id);
    }

    public List<String> search(String q, long limit) {
        return List.of(q);
    }

    int countPending() {
        return 0;
    }
}
`

// javaFixture is the AC10 Spring project: the controller and a mapper (wildcard import) in
// com.acme.web, the service and its package-private repository in com.acme.svc, the service's
// test in the same package under the test source root, a static-import target in
// com.acme.util, a scheduled job, and a target/ copy of the controller and the service that the
// walk must not enter.
func javaFixture() map[string]string {
	return map[string]string{
		javaController: javaOrderController,
		javaMapper: `package com.acme.web;

import com.acme.svc.*;

public interface OrderMapper {
    String toView(OrderService service);
}
`,
		javaService: javaOrderService,
		javaRepository: `package com.acme.svc;

class OrderRepository {
    String load(String id) {
        return id;
    }
}
`,
		javaServiceTst: `package com.acme.svc;

import org.junit.jupiter.api.Test;

class OrderServiceTest {
    @Test
    void countsPending() {
        OrderService service = new OrderService();
        assertEquals(0, service.countPending());
    }
}
`,
		javaIds: `package com.acme.util;

public final class Ids {
    private static long counter;

    public static long next() {
        return ++counter;
    }
}
`,
		javaJob: `package com.acme.jobs;

@Component
public class CleanupJob {
    @Scheduled(cron = "0 */5 * * * *")
    public void purge() {
    }

    @Scheduled(fixedRate = 5000)
    public void heartbeat() {
    }
}
`,
		"target/generated-sources/com/acme/web/OrderController.java": javaOrderController,
		"target/generated-sources/com/acme/svc/OrderService.java":    javaOrderService,
	}
}

// javaFilePackages are the file_packages rows of the AC10 fixture ("path key").
var javaFilePackages = []string{
	javaJob + " java:com.acme.jobs",
	javaRepository + " java:com.acme.svc",
	javaService + " java:com.acme.svc",
	javaIds + " java:com.acme.util",
	javaController + " java:com.acme.web",
	javaMapper + " java:com.acme.web",
	javaServiceTst + " java:com.acme.svc",
}

// javaEdges are the file_edges of the AC10 fixture: the single-type import, the static import to
// its class file, the wildcard import to every file of com.acme.svc (both source roots); none for
// java.util.List or org.junit.
var javaEdges = []string{
	javaController + " -> " + javaService,
	javaController + " -> " + javaIds,
	javaMapper + " -> " + javaRepository,
	javaMapper + " -> " + javaService,
	javaMapper + " -> " + javaServiceTst,
}

// javaFacts are the file_facts rows of the AC10 fixture ("path endpoints crons").
var javaFacts = []string{
	javaJob + ` [] ["0 */5 * * * *", "fixedRate=5000"]`,
	javaController + ` ["GET /api/orders/{id}", "POST /api/orders/search"] []`,
}

// facts returns the repo's file_facts rows as sorted "path endpoints crons" strings.
func (e *env) facts() []string {
	e.t.Helper()
	rows := e.scanStrings(`SELECT file_path || ' ' || endpoints::text || ' ' || crons::text FROM file_facts WHERE repo_id = ?`, e.repo)
	slices.Sort(rows)
	return rows
}

// AC10, AC11: a full index of the Spring project indexes every source file outside target/ with
// its classes, nested class, methods and constructors, and the import edges of single-type,
// wildcard and static imports.
func TestJava_FullIndex(t *testing.T) {
	e := newEnv(t, withFiles(javaFixture()))
	e.mustFull()

	st := e.mustState()
	if st.Status != domain.StatusFull || st.FilesIndexed != 7 {
		t.Errorf("state = {%s files_indexed %d}, want {full 7}", st.Status, st.FilesIndexed)
	}
	if got := statInt(t, st, "totalCandidates"); got != 7 {
		t.Errorf("stats.totalCandidates = %d, want 7 (target/ not counted)", got)
	}
	if degraded, ok := st.Stats["parseDegraded"].([]any); !ok || len(degraded) != 0 {
		t.Errorf("stats.parseDegraded = %#v, want an empty array", st.Stats["parseDegraded"])
	}
	for _, c := range []struct{ stat, table string }{
		{"symbolsWritten", "symbols"}, {"referencesWritten", `"references"`}, {"edgesWritten", "file_edges"},
		{"factsWritten", "file_facts"},
	} {
		if got, want := statInt(t, st, c.stat), e.count(c.table); got != want || want == 0 {
			t.Errorf("stats.%s = %d, rows in %s = %d, want equal and non-zero", c.stat, got, c.table, want)
		}
	}
	for _, table := range []struct{ name, column string }{
		{"symbols", "path"}, {`"references"`, "from_path"}, {"file_facts", "file_path"}, {"file_packages", "file_path"},
		{"file_rank", "file_path"}, {"file_edges", "from_file"}, {"file_edges", "to_file"},
	} {
		if slices.ContainsFunc(e.paths(table.name, table.column), func(p string) bool { return strings.HasPrefix(p, "target/") }) {
			t.Errorf("%s.%s holds a target/ path, want the build output left out", table.name, table.column)
		}
	}

	// AC10: classes, the nested class, methods and constructors; exported from public or
	// interface membership; annotations are not part of a signature.
	wantSymbols := []string{
		javaJob + "|CleanupJob|class|3|12|true|public class CleanupJob",
		javaJob + "|heartbeat|method|9|11|true|public void heartbeat()",
		javaJob + "|purge|method|5|7|true|public void purge()",
		javaRepository + "|OrderRepository|class|3|7|false|class OrderRepository",
		javaRepository + "|load|method|4|6|false|String load(String id)",
		javaService + "|OrderService|class|5|19|true|public class OrderService",
		javaService + "|countPending|method|16|18|false|int countPending()",
		javaService + "|find|method|8|10|true|public String find(String id)",
		javaService + "|search|method|12|14|true|public List<String> search(String q, long limit)",
		javaIds + "|Ids|class|3|9|true|public final class Ids",
		javaIds + "|next|method|6|8|true|public static long next()",
		javaController + "|OrderController|class|7|37|true|public class OrderController",
		javaController + "|OrderController|method|18|20|true|public OrderController(OrderService service)",
		javaController + "|Page|class|14|16|false|static class Page",
		javaController + "|get|method|22|25|true|public String get(@PathVariable String id)",
		javaController + "|legacy|method|33|36|false|String legacy()",
		javaController + "|search|method|27|31|true|public List<String> search(String q)",
		javaMapper + "|OrderMapper|interface|5|7|true|public interface OrderMapper",
		javaMapper + "|toView|method|6|6|true|String toView(OrderService service)",
		javaServiceTst + "|OrderServiceTest|class|5|11|false|class OrderServiceTest",
		javaServiceTst + "|countsPending|method|6|10|false|void countsPending()",
	}
	gotSymbols := e.scanStrings(`SELECT path || '|' || name || '|' || kind || '|' || line::text || '|' || end_line::text || '|' ||
		exported::text || '|' || coalesce(signature, '') FROM symbols WHERE repo_id = ?`, e.repo)
	slices.Sort(gotSymbols)
	if diff := cmp.Diff(wantSymbols, gotSymbols); diff != "" {
		t.Errorf("symbols mismatch (-want +got):\n%s", diff)
	}

	// AC11
	if diff := cmp.Diff(javaEdges, e.edges()); diff != "" {
		t.Errorf("file_edges mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(javaFilePackages, e.filePackages()); diff != "" {
		t.Errorf("file_packages mismatch (-want +got):\n%s", diff)
	}
	// References resolved through each kind of import edge.
	tests := []struct {
		name, from, symbol string
		want               []string
	}{
		{"single-type import", javaController, "find", []string{javaService}},
		{"static import", javaController, "next", []string{javaIds}},
		{"JDK call stays unresolved", javaService, "of", []string{"NULL"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, e.declFiles(tc.from, tc.symbol)); diff != "" {
				t.Errorf("decl_file of %s in %s mismatch (-want +got):\n%s", tc.symbol, tc.from, diff)
			}
		})
	}
}

// AC12: a package-private method called from the test source root resolves through the shared
// package key to the main source root; so does a class of the same package.
func TestJava_SamePackageAcrossSourceRoots(t *testing.T) {
	e := newEnv(t, withFiles(javaFixture()))
	e.mustFull()
	tests := []struct {
		name, from, symbol string
		want               []string
	}{
		{"package-private method from the test root", javaServiceTst, "countPending", []string{javaService}},
		{"class of the package from the test root", javaServiceTst, "OrderService", []string{javaService}},
		{"package-private class in the main root", javaService, "OrderRepository", []string{javaRepository}},
		{"package-private method in the main root", javaService, "load", []string{javaRepository}},
		{"library call stays unresolved", javaServiceTst, "assertEquals", []string{"NULL"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, e.declFiles(tc.from, tc.symbol)); diff != "" {
				t.Errorf("decl_file of %s in %s mismatch (-want +got):\n%s", tc.symbol, tc.from, diff)
			}
		})
	}
}

// AC13, AC14: the controller's endpoints and the job's crons are its facts (the non-literal path
// gives none); the blast radius of the service lists the controller with both endpoints.
func TestJava_FactsAndBlast(t *testing.T) {
	e := newEnv(t, withFiles(javaFixture()))
	e.mustFull()

	// AC13
	if diff := cmp.Diff(javaFacts, e.facts()); diff != "" {
		t.Errorf("file_facts mismatch (-want +got):\n%s", diff)
	}

	// AC14
	got := e.blast(e.reader, javaService)
	if got.Degraded || got.Reason != "" {
		t.Errorf("BlastRadius(OrderService.java) = degraded %v reason %q, want complete", got.Degraded, got.Reason)
	}
	for _, name := range []string{"OrderService", "find", "search", "countPending"} {
		if !slices.ContainsFunc(got.ChangedSymbols, func(s domain.BlastSymbol) bool { return s.File == javaService && s.Name == name }) {
			t.Errorf("BlastRadius(OrderService.java) changed symbols = %+v, want %s among them", got.ChangedSymbols, name)
		}
	}
	var callers []string
	for _, c := range got.Callers {
		callers = append(callers, fmt.Sprintf("%s %s via %s", c.File, c.Symbol, c.ViaSymbol))
	}
	slices.Sort(callers)
	wantCallers := []string{
		javaController + " get via find",
		javaController + " search via search",
		javaServiceTst + " countsPending via OrderService",
		javaServiceTst + " countsPending via countPending",
	}
	if diff := cmp.Diff(wantCallers, callers); diff != "" {
		t.Errorf("BlastRadius(OrderService.java) callers mismatch (-want +got):\n%s", diff)
	}
	wantFacts := domain.BlastFacts{Endpoints: []string{"GET /api/orders/{id}", "POST /api/orders/search"}, Crons: []string{}}
	if diff := cmp.Diff(wantFacts, got.FactsByFile[javaController]); diff != "" {
		t.Errorf("BlastRadius(OrderService.java) facts of the controller mismatch (-want +got):\n%s", diff)
	}

	// FR27: the clone scan for callers reads the Java sources and never target/.
	r, log := e.callerReader(0)
	sigs, err := r.CallerSignatures(t.Context(), e.repo, []string{javaService}, 10)
	if err != nil {
		t.Fatalf("CallerSignatures(OrderService.java) error = %v", err)
	}
	if !slices.ContainsFunc(sigs, func(s domain.CallerSignature) bool { return s.File == javaController }) {
		t.Errorf("CallerSignatures(OrderService.java) = %+v, want the controller among them", sigs)
	}
	for _, s := range sigs {
		if strings.HasPrefix(s.File, "target/") {
			t.Errorf("CallerSignatures(OrderService.java) returned %+v, want nothing under target/", s)
		}
	}
	for _, p := range log.paths {
		if strings.HasPrefix(p, "target/") {
			t.Errorf("the caller scan opened %q, want nothing under target/", p)
		}
	}
}

// AC15: Java and TypeScript in one clone are indexed in one state; the TypeScript rows equal
// those of a TypeScript-only index of baseFiles (the fixture of full_test.go), and the Java rows
// equal those of the Java-only index.
func TestMixed_JavaAndTS(t *testing.T) {
	tsOnly := newEnv(t)
	tsOnly.mustFull()

	files := baseFiles()
	maps.Copy(files, javaFixture())
	e := newEnv(t, withFiles(files))
	e.mustFull()

	st := e.mustState()
	if st.Status != domain.StatusFull || st.IndexerVersion != domain.IndexerVersion || st.FilesIndexed != 15 {
		t.Errorf("state = {%s v%d files_indexed %d}, want {full v%d 15}", st.Status, st.IndexerVersion, st.FilesIndexed, domain.IndexerVersion)
	}
	if got := statInt(t, st, "filesSeen"); got != 15 {
		t.Errorf("stats.filesSeen = %d, want 15 (8 TypeScript + 7 Java)", got)
	}
	if got := e.count("repo_index_state"); got != 1 {
		t.Errorf("repo_index_state rows = %d, want 1", got)
	}
	for _, c := range []struct{ stat, table string }{
		{"symbolsWritten", "symbols"}, {"referencesWritten", `"references"`}, {"edgesWritten", "file_edges"},
		{"ranked", "file_rank"}, {"factsWritten", "file_facts"},
	} {
		if got, want := statInt(t, st, c.stat), e.count(c.table); got != want || want == 0 {
			t.Errorf("stats.%s = %d, rows in %s = %d, want equal and non-zero", c.stat, got, c.table, want)
		}
	}

	// The TypeScript rows of today: symbols, references with their decl_file, edges and facts.
	for _, table := range []struct{ name, column string }{
		{"symbols", "path"}, {`"references"`, "from_path"}, {"file_edges", "from_file"}, {"file_facts", "file_path"},
	} {
		want := tsOnly.nonJavaRows(table.name, table.column)
		if len(want) == 0 {
			t.Errorf("TypeScript-only index has no %s rows, want some to compare", table.name)
		}
		if diff := cmp.Diff(want, e.nonJavaRows(table.name, table.column)); diff != "" {
			t.Errorf("TypeScript rows of %s differ from the TypeScript-only index (-ts only +mixed):\n%s", table.name, diff)
		}
	}
	endpoints := e.scanStrings(`SELECT endpoints::text FROM file_facts WHERE repo_id = ? AND file_path = 'src/server.ts'`, e.repo)
	if diff := cmp.Diff([]string{`["GET /x"]`}, endpoints); diff != "" {
		t.Errorf("file_facts.endpoints of src/server.ts mismatch (-want +got):\n%s", diff)
	}

	// The Java rows of the Java-only index, and no edge across the languages.
	var javaEdgesGot []string
	for _, edge := range e.edges() {
		from, to, _ := strings.Cut(edge, " -> ")
		if strings.HasSuffix(from, ".java") != strings.HasSuffix(to, ".java") {
			t.Errorf("file_edges holds %q, want no edge between TypeScript and Java", edge)
		}
		if strings.HasSuffix(from, ".java") {
			javaEdgesGot = append(javaEdgesGot, edge)
		}
	}
	if diff := cmp.Diff(javaEdges, javaEdgesGot); diff != "" {
		t.Errorf("Java file_edges mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(javaFilePackages, e.filePackages()); diff != "" {
		t.Errorf("file_packages mismatch, want only the Java files (-want +got):\n%s", diff)
	}
	var javaFactsGot []string
	for _, f := range e.facts() {
		if strings.HasPrefix(f, "src/main/java/") {
			javaFactsGot = append(javaFactsGot, f)
		}
	}
	if diff := cmp.Diff(javaFacts, javaFactsGot); diff != "" {
		t.Errorf("Java file_facts mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{javaService}, e.declFiles(javaServiceTst, "countPending")); diff != "" {
		t.Errorf("decl_file of countPending in the test mismatch (-want +got):\n%s", diff)
	}
}

// nonJavaRows returns the repo's rows of table whose column does not name a .java file, as sorted
// JSON documents without the repo and row ids.
func (e *env) nonJavaRows(table, column string) []string {
	e.t.Helper()
	rows := e.scanStrings(fmt.Sprintf(`SELECT (to_jsonb(x) - 'repo_id' - 'id')::text FROM %s x WHERE repo_id = ? AND %s NOT LIKE ?`, table, column),
		e.repo, "%.java")
	slices.Sort(rows)
	return rows
}

// S-M2: a 3.4 KB package declaration made a package key Postgres refused in the package-key index
// ("index row size exceeds btree maximum 2704"), which rolled the whole index back for good. The
// file now gets no package row, and the repo indexes full with every other row in place.
func TestJava_LongPackageDeclarationStillIndexes(t *testing.T) {
	const longFile = "src/main/java/Long.java"
	// Hash segments: a repetitive name compresses below the index row limit and stores fine.
	var segs []string
	for i := range 100 {
		h := sha256.Sum256([]byte(strconv.Itoa(i)))
		segs = append(segs, "p"+hex.EncodeToString(h[:16]))
	}
	files := javaFixture()
	files[longFile] = "package " + strings.Join(segs, ".") + ";\n\npublic class Long {\n    public void run() {\n    }\n}\n"
	e := newEnv(t, withFiles(files))
	clean := newEnv(t, withFiles(javaFixture()))
	clean.mustFull()

	e.mustFull()

	st := e.mustState()
	if st.Status != domain.StatusFull || st.FilesIndexed != 8 {
		t.Fatalf("state = {%s files_indexed %d} (stats %v), want {full 8}", st.Status, st.FilesIndexed, st.Stats)
	}
	if diff := cmp.Diff(clean.paths("file_packages", "file_path || ' ' || package_key"),
		e.paths("file_packages", "file_path || ' ' || package_key")); diff != "" {
		t.Errorf("file_packages mismatch with the fixture without the long package (-want +got):\n%s", diff)
	}
	want := append(clean.symbolNames(), longFile+":Long", longFile+":run")
	slices.Sort(want)
	if diff := cmp.Diff(want, e.symbolNames()); diff != "" {
		t.Errorf("symbols mismatch (-want +got):\n%s", diff)
	}
}
