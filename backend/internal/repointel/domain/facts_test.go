package domain

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestExtractEndpoints(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"none", "const a = 1;\n", []string{}},
		{"express verbs", "app.get('/a', h)\nrouter.post(\"/b\", h)\nfastify.delete(`/c`, h)", []string{"GET /a", "POST /b", "DELETE /c"}},
		{"all verbs", "api.options('/o')\nserver.head('/h')\napp.put('/p')\napp.patch('/q')", []string{"OPTIONS /o", "HEAD /h", "PUT /p", "PATCH /q"}},
		{"generics and spaces", "app.get<{ Params: P }>('/x/:id', h)\napp . get", []string{"GET /x/:id"}},
		{"whitespace before paren", "app.get  <T>  (  '/w')", []string{"GET /w"}},
		{"mismatched quotes", "app.get('/a\", h)\napp.get(\"/b', h)", []string{}},
		{"case insensitive", "APP.GET('/u')\nRouter.Post('/v')", []string{"GET /u", "POST /v"}},
		{"word boundary", "myapp.get('/no')\nxapp.get('/no')", []string{}},
		{"route object", "route({ method: 'get', url: '/o1' })\n{ method: \"POST\", handler, url: `/o2` }", []string{"GET /o1", "POST /o2"}},
		{"route object other verb", "{ method: 'OPTIONS', url: '/no' }", []string{}},
		{"route object split over lines", "{ method: 'GET',\n url: '/no' }", []string{}},
		{"both on one line", "app.get('/a', () => ({ method: 'POST', url: '/b' }))", []string{"GET /a", "POST /b"}},
		{"duplicates first-seen", "app.get('/a')\napp.post('/b')\napp.get('/a')", []string{"GET /a", "POST /b"}},
		{"crlf", "app.get('/a')\r\napp.post('/b')\r\n", []string{"GET /a", "POST /b"}},
		{"nbsp as space", "app.get\u00a0('/n')", []string{"GET /n"}},
		{"long s is not s", "app.get('/a')\napp.po\u017ft('/b')\n\u017eerver.get('/c')", []string{"GET /a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, ExtractEndpoints(tc.in)); diff != "" {
				t.Errorf("ExtractEndpoints(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestExtractCrons(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"none", "const a = 1;", []string{}},
		{"cron schedule", "cron.schedule('*/5 * * * *', run)", []string{"*/5 * * * *"}},
		{"schedule call", "schedule(\"0 3 * * *\", run)\nnew CronJob(`0 0 1 1 *`, f)", []string{"0 3 * * *", "0 0 1 1 *"}},
		{"digits form", "cron('10 20')", []string{"10 20"}},
		{"expression is trimmed", "cron('  * * * * *  ')", []string{"* * * * *"}},
		{"no star or digit pair", "cron('daily')\nschedule('5')", []string{}},
		{"mismatched quote still matches", "cron('* * * * *\")", []string{"* * * * *"}},
		{"job kind with job word", "jobs.register('poll_repos', handler)", []string{"job:poll_repos"}},
		{"job kind with workspace arg", "await queue.enqueue(ws, 'clone_repo')  // clone", []string{"job:clone_repo"}},
		{"job kind needs job word", "registry.register('thing', x)", []string{}},
		{"job word on the line, not the kind", "jobs.register('Thing', x)", []string{"job:Thing"}},
		{"kind must start with a letter", "jobs.register('_x')\njobs.register('9x')", []string{}},
		{"kind characters", "jobs.register('Ab_9-c')", []string{}},
		{"case insensitive", "CRON.SCHEDULE('* * * * *')\nJOBS.REGISTER('digest')", []string{"* * * * *", "job:digest"}},
		{"duplicates first-seen", "cron('* * * * *')\njobs.register('sync')\ncron('* * * * *')\njobs.register('sync')", []string{"* * * * *", "job:sync"}},
		{"both on one line", "schedule('0 1 * * *'); jobs.register('index')", []string{"0 1 * * *", "job:index"}},
		{"long s is not s", "jobs.regi\u017eter('sync')\njobs.\u017fync", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, ExtractCrons(tc.in)); diff != "" {
				t.Errorf("ExtractCrons(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestExtractFacts(t *testing.T) {
	if _, ok := ExtractFacts("a.ts", "const a = 1"); ok {
		t.Error("ExtractFacts(no facts) ok = true, want false")
	}
	got, ok := ExtractFacts("api.ts", "app.get('/a')\nschedule('* * * * *')")
	want := FileFacts{Path: "api.ts", Endpoints: []string{"GET /a"}, Crons: []string{"* * * * *"}}
	if !ok {
		t.Fatal("ExtractFacts(facts) ok = false, want true")
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ExtractFacts mismatch (-want +got):\n%s", diff)
	}
	// One kind alone still keeps the row, and the other list is an empty array.
	got, ok = ExtractFacts("c.ts", "app.get('/a')")
	if !ok || got.Crons == nil || len(got.Crons) != 0 {
		t.Errorf("ExtractFacts(endpoint only) = %+v, %v; want ok and a non-nil empty Crons", got, ok)
	}
}
