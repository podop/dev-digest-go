package domain

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestJavaAnnotations(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []Annotation
	}{
		{"marker", "@Override\nvoid f() {}", []Annotation{{Name: "Override", Line: 1}}},
		{"args across lines",
			"class A {\n  @RequestMapping(value = \"/s\",\n      method = RequestMethod.POST)\n  void f() {}\n}",
			[]Annotation{{Name: "RequestMapping", Args: "value = \"/s\",\n      method = RequestMethod.POST", Line: 2}}},
		{"several with lines", "@A\n\n@B(1)\n@C(x = {1, 2})",
			[]Annotation{{Name: "A", Line: 1}, {Name: "B", Args: "1", Line: 3}, {Name: "C", Args: "x = {1, 2}", Line: 4}}},
		{"qualified name", `@org.springframework.GetMapping("/x")`,
			[]Annotation{{Name: "org.springframework.GetMapping", Args: `"/x"`, Line: 1}}},
		{"empty parens", "@Foo()", []Annotation{{Name: "Foo", Line: 1}}},
		{"space before paren", "@Foo (1)", []Annotation{{Name: "Foo", Args: "1", Line: 1}}},
		{"nested parens", "@A(f(g(1)), (2))", []Annotation{{Name: "A", Args: "f(g(1)), (2)", Line: 1}}},
		{"inner annotation stays in args", `@A(@B("x")) @C`,
			[]Annotation{{Name: "A", Args: `@B("x")`, Line: 1}, {Name: "C", Line: 1}}},
		{"parameter annotations", `void f(@PathVariable("id") String id, @RequestBody B b) {}`,
			[]Annotation{{Name: "PathVariable", Args: `"id"`, Line: 1}, {Name: "RequestBody", Line: 1}}},
		{"annotation type declaration is not an annotation", "public @interface Marker {}", []Annotation{}},
		{"lone at", "a @ ( b", []Annotation{}},
		{"empty", "", []Annotation{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := JavaAnnotations(tc.in)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("JavaAnnotations(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

// Review Focus 3: ")" "(" and "@" inside literals and comments neither split nor invent
// annotations.
func TestJavaAnnotations_LiteralsAndComments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []Annotation
	}{
		{"paren in string", `@A(")") @B`, []Annotation{{Name: "A", Args: `")"`, Line: 1}, {Name: "B", Line: 1}}},
		{"paren in char", `@A('(') @B`, []Annotation{{Name: "A", Args: `'('`, Line: 1}, {Name: "B", Line: 1}}},
		{"at in string", `@A("@x") @B`, []Annotation{{Name: "A", Args: `"@x"`, Line: 1}, {Name: "B", Line: 1}}},
		{"escaped quote", `@A("\")") @B`, []Annotation{{Name: "A", Args: `"\")"`, Line: 1}, {Name: "B", Line: 1}}},
		{"quote in char", `@A('"') @B`, []Annotation{{Name: "A", Args: `'"'`, Line: 1}, {Name: "B", Line: 1}}},
		{"escaped char", `@A('\'', ')') @B`, []Annotation{{Name: "A", Args: `'\'', ')'`, Line: 1}, {Name: "B", Line: 1}}},
		{"text block", "@A(\"\"\"\n  a)b @Fake(\n  \"\"\") @B",
			[]Annotation{{Name: "A", Args: "\"\"\"\n  a)b @Fake(\n  \"\"\"", Line: 1}, {Name: "B", Line: 3}}},
		{"text block with escaped quotes", "@A(\"\"\"\n \\\"\"\") @Fake(\n\"\"\") @B",
			[]Annotation{{Name: "A", Args: "\"\"\"\n \\\"\"\") @Fake(\n\"\"\"", Line: 1}, {Name: "B", Line: 3}}},
		{"line comment", "// @Fake(\n@B", []Annotation{{Name: "B", Line: 2}}},
		{"block comment", "/* @Fake( */ @B", []Annotation{{Name: "B", Line: 1}}},
		{"multi-line block comment", "/*\n * @Fake(\n */\n@B", []Annotation{{Name: "B", Line: 4}}},
		{"comment inside args", "@A(1 /* ) */, // )\n 2) @B",
			[]Annotation{{Name: "A", Args: "1 /* ) */, // )\n 2", Line: 1}, {Name: "B", Line: 2}}},
		{"string in code", "String s = \"@Fake(\";\n@B", []Annotation{{Name: "B", Line: 2}}},
		{"unterminated string ends at the line", "String s = \"abc\n@B", []Annotation{{Name: "B", Line: 2}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := JavaAnnotations(tc.in)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("JavaAnnotations(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

// EC10: an argument list that never closes, or is longer than MaxAnnotationBytes, is skipped and
// scanning continues after its @Name token.
func TestJavaAnnotations_UnclosedOrTooLong(t *testing.T) {
	long := strings.Repeat("x", MaxAnnotationBytes+1)
	limit := strings.Repeat("x", MaxAnnotationBytes)
	tests := []struct {
		name string
		in   string
		want []Annotation
	}{
		{"unclosed", "@A(\"x\"\n@B(\"y\")", []Annotation{{Name: "B", Args: `"y"`, Line: 2}}},
		{"unclosed at end", "@B\n@A(", []Annotation{{Name: "B", Line: 1}}},
		{"too long", "@A(" + long + ")\n@B", []Annotation{{Name: "B", Line: 2}}},
		{"at the limit", "@A(" + limit + ")", []Annotation{{Name: "A", Args: limit, Line: 1}}},
		{"too long keeps inner annotations", "@A(@C(1) " + long + ") @B",
			[]Annotation{{Name: "C", Args: "1", Line: 1}, {Name: "B", Line: 1}}},
		{"unclosed keeps following mapping", "@Deprecated(\nclass A {\n @GetMapping(\"/x\") void f() {}\n}",
			[]Annotation{{Name: "GetMapping", Args: `"/x"`, Line: 3}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := JavaAnnotations(tc.in)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("JavaAnnotations(%.60q…) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

// springController wraps method declarations in the AC10 class prefix.
func springController(body string) string {
	return "package com.acme.web;\n\n@RestController\n@RequestMapping(\"/api/orders\")\npublic class OrderController {\n" +
		body + "\n}\n"
}

type springCase struct {
	name      string
	in        string
	endpoints []string
	crons     []string
}

// springCases is shared with FuzzJavaFacts as its seed corpus.
var springCases = []springCase{
	{"AC10 controller", springController("    static class Nested {}\n" +
		"    @GetMapping(\"/{id}\")\n    Order get(@PathVariable String id) { return null; }\n" +
		"    @RequestMapping(value = \"/search\",\n        method = RequestMethod.POST)\n    List<Order> search() { return null; }"),
		[]string{"GET /api/orders/{id}", "POST /api/orders/search"}, []string{}},
	{"path array", springController(`@PutMapping(path = {"/a", "/b"}) void put() {}`),
		[]string{"PUT /api/orders/a", "PUT /api/orders/b"}, []string{}},
	{"method array", springController(`@RequestMapping(value="/m", method={RequestMethod.GET, RequestMethod.HEAD}) void m() {}`),
		[]string{"GET /api/orders/m", "HEAD /api/orders/m"}, []string{}},
	{"no-arg delete", springController("@DeleteMapping void d() {}"), []string{"DELETE /api/orders"}, []string{}},
	{"request mapping without method", springController(`@RequestMapping("/any") void a() {}`),
		[]string{"ANY /api/orders/any"}, []string{}},
	{"static-import method", springController(`@RequestMapping(path = "/p", method = POST) void p() {}`),
		[]string{"POST /api/orders/p"}, []string{}},
	{"patch and post", springController("@PatchMapping(\"/x\") void a() {}\n@PostMapping(value = \"/y\") void b() {}"),
		[]string{"PATCH /api/orders/x", "POST /api/orders/y"}, []string{}},
	{"named args without path", springController(`@GetMapping(produces = "application/json") void g() {}`),
		[]string{"GET /api/orders"}, []string{}},
	{"empty array is the empty path", springController(`@GetMapping({}) void g() {}`),
		[]string{"GET /api/orders"}, []string{}},
	{"qualified annotation", springController(`@org.springframework.web.bind.annotation.GetMapping("/q") void q() {}`),
		[]string{"GET /api/orders/q"}, []string{}},
	{"duplicates", springController("@GetMapping(\"/d\") void a() {}\n@GetMapping(\"/d\") void b() {}"),
		[]string{"GET /api/orders/d"}, []string{}},
	{"one slash between", "@RequestMapping(\"/api/\")\nclass C {\n@GetMapping(\"//x\") void a() {}\n@GetMapping(\"y\") void b() {}\n}",
		[]string{"GET /api/x", "GET /api/y"}, []string{}},
	{"no class prefix", "public class C {\n@GetMapping(\"/x\") void a() {}\n@PostMapping(\"y\") void b() {}\n}",
		[]string{"GET /x", "POST /y"}, []string{}},
	{"method request mapping after the class is no prefix",
		"public class C {\n@RequestMapping(\"/r\") void a() {}\n@GetMapping(\"/x\") void b() {}\n}",
		[]string{"ANY /r", "GET /x"}, []string{}},
	{"prefix only with no-arg mapping", "@RequestMapping\nclass C {\n@GetMapping void g() {}\n}", []string{"GET /"}, []string{}},
	{"interface prefix", "@RequestMapping(\"/api\")\npublic interface Api {\n@GetMapping(\"/x\") X get();\n}",
		[]string{"GET /api/x"}, []string{}},
	{"class literal is no declaration", "@Import(Cfg.class)\n@RequestMapping(\"/v\")\nclass C {\n@GetMapping(\"/x\") void g() {}\n}",
		[]string{"GET /v/x"}, []string{}},
	{"concatenation", springController(`@GetMapping(BASE + "/x") void g() {}`), []string{}, []string{}},
	{"constant", springController(`@GetMapping(BASE) void g() {}`), []string{}, []string{}},
	{"literal kept beside a constant", springController(`@GetMapping({"/a", BASE}) void g() {}`),
		[]string{"GET /api/orders/a"}, []string{}},
	{"non-literal prefix", "@RequestMapping(BASE)\nclass C {\n@GetMapping(\"/x\") void g() {}\n}", []string{}, []string{}},
	{"unknown method", springController(`@RequestMapping(value = "/u", method = M) void u() {}`), []string{}, []string{}},
	{"mapping in comment", springController("// @GetMapping(\"/fake\")\n/* @PostMapping(\"/fake\") */ @GetMapping(\"/real\") void g() {}"),
		[]string{"GET /api/orders/real"}, []string{}},
	{"cron", "class J {\n@Scheduled(cron = \"0 */5 * * * *\") void a() {}\n}", []string{}, []string{"0 */5 * * * *"}},
	{"cron placeholder", "class J {\n@Scheduled(cron = \"${jobs.cron}\") void a() {}\n}", []string{}, []string{"${jobs.cron}"}},
	{"fixed rate", "class J {\n@Scheduled(fixedRate = 5000) void a() {}\n}", []string{}, []string{"fixedRate=5000"}},
	{"fixed delay string", "class J {\n@Scheduled(fixedDelayString = \"PT5M\") void a() {}\n}", []string{}, []string{"fixedDelayString=PT5M"}},
	{"fixed delay with initial delay", "class J {\n@Scheduled(fixedDelay = 1000L, initialDelay = 500) void a() {}\n}",
		[]string{}, []string{"fixedDelay=1000L"}},
	{"fixed rate string and cron", "class J {\n@Scheduled(cron = \"0 0 * * * *\", zone = \"UTC\")\n@Scheduled(fixedRateString = \"${r}\") void a() {}\n}",
		[]string{}, []string{"0 0 * * * *", "fixedRateString=${r}"}},
	{"cron constant", "class J {\n@Scheduled(cron = CRON) void a() {}\n}", []string{}, []string{}},
	{"fixed rate expression", "class J {\n@Scheduled(fixedRate = 5 * 1000) void a() {}\n}", []string{}, []string{}},
	{"TS patterns ignored", "class J {\n// x\n}\napp.get('/x', h)\nschedule('* * * * *')", []string{}, []string{}},
}

func TestExtractFacts_Spring(t *testing.T) {
	for _, tc := range springCases {
		t.Run(tc.name, func(t *testing.T) {
			const path = "src/main/java/com/acme/web/OrderController.java"
			got, ok := ExtractFacts(path, tc.in)
			wantOK := len(tc.endpoints)+len(tc.crons) > 0
			if ok != wantOK {
				t.Fatalf("ExtractFacts(%q) ok = %v, want %v (facts %+v)", tc.in, ok, wantOK, got)
			}
			if !ok {
				return
			}
			want := FileFacts{Path: path, Endpoints: tc.endpoints, Crons: tc.crons}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("ExtractFacts(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestJoinPath(t *testing.T) {
	tests := []struct{ prefix, path, want string }{
		{"/api/orders", "/{id}", "/api/orders/{id}"},
		{"/api/orders", "", "/api/orders"},
		{"/api/orders/", "/x", "/api/orders/x"},
		{"", "", "/"},
		{"/", "", "/"},
		{"", "/x", "/x"},
		{"", "x", "/x"},
		{"api", "x", "/api/x"},
		{"//a//", "//b/", "//a/b/"},
	}
	for _, tc := range tests {
		if got := joinPath(tc.prefix, tc.path); got != tc.want {
			t.Errorf("joinPath(%q, %q) = %q, want %q", tc.prefix, tc.path, got, tc.want)
		}
	}
}
