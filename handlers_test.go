package people

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/camwillm/gluecozs-sdk"
)

// fakeHost wraps bodies in a marker instead of the real layout.
type fakeHost struct{ ai sdk.AI }

func (fakeHost) Page(w http.ResponseWriter, _ *http.Request, status int, navID, title string, body template.HTML) {
	w.WriteHeader(status)
	fmt.Fprintf(w, "<html data-nav=%q data-title=%q>%s</html>", navID, title, body)
}
func (h fakeHost) AI() sdk.AI            { return h.ai }
func (fakeHost) Speech() sdk.Transcriber { return nil }

// fakeAI replies with a canned string, or an error.
type fakeAI struct {
	reply  string
	err    error
	prompt string
}

func (f *fakeAI) Available() bool { return true }
func (f *fakeAI) Ask(_ context.Context, system, prompt string) (string, error) {
	f.prompt = system + "\n---\n" + prompt
	return f.reply, f.err
}

type env struct {
	m   *Module
	mux *http.ServeMux
	ai  *fakeAI
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s := testStore(t)
	m := New(s, nyc)
	m.now = func() time.Time { return now }
	ai := &fakeAI{}
	mux := http.NewServeMux()
	m.Mount(mux, fakeHost{ai: ai})
	return &env{m: m, mux: mux, ai: ai}
}

func (e *env) get(t *testing.T, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(b)
}

func (e *env) post(t *testing.T, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func TestAddEditLogDeleteThroughTheForms(t *testing.T) {
	e := newEnv(t)

	rec := e.post(t, "/m/people/new", url.Values{
		"name": {"Kwame"}, "company": {"DataCo"}, "city": {"DC"},
		"tags": {"tech, vibes"}, "why": {"tech: honest DS advice\nvibes: big-brother energy"},
		"cadence": {"30"}, "birthday": {"03-14"}, "how_met": {"Discord"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	p, ok := e.m.store.Get("p1")
	if !ok || p.Why["tech"] != "honest DS advice" || p.CadenceDays != 30 {
		t.Fatalf("stored person: %+v", p)
	}

	// A bad why-line is rejected with the form shown again.
	rec = e.post(t, "/m/people/new", url.Values{"name": {"X"}, "why": {"no colon here"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "tag: why they matter") {
		t.Errorf("bad why line: %d", rec.Code)
	}

	// The detail page shows the why rows and the bucket chips.
	code, page := e.get(t, "/m/people/p?id=p1")
	for _, want := range []string{"Kwame", "honest DS advice", "big-brother energy", "Discord", `data-nav="people"`} {
		if !strings.Contains(page, want) {
			t.Errorf("detail page is missing %q", want)
		}
	}
	if code != 200 {
		t.Fatalf("detail: %d", code)
	}

	// Log a touch from the quick button: last contact moves to today.
	rec = e.post(t, "/m/people/touch", url.Values{"id": {"p1"}, "kind": {"text"}, "from": {"list"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("touch: %d", rec.Code)
	}
	p, _ = e.m.store.Get("p1")
	if p.LastContact != "2026-11-16" {
		t.Errorf("last contact = %q", p.LastContact)
	}

	// Edit keeps the id and saves changes.
	rec = e.post(t, "/m/people/update", url.Values{
		"id": {"p1"}, "name": {"Kwame"}, "tags": {"tech"}, "pipeline": {"chatted"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	p, _ = e.m.store.Get("p1")
	if p.Pipeline != "chatted" || len(p.Tags) != 1 {
		t.Errorf("after edit: %+v", p)
	}

	rec = e.post(t, "/m/people/delete", url.Values{"id": {"p1"}})
	if rec.Code != http.StatusSeeOther || e.m.store.Len() != 0 {
		t.Errorf("delete: %d, %d left", rec.Code, e.m.store.Len())
	}
}

func TestListPageSectionsAndGrouping(t *testing.T) {
	e := newEnv(t)
	add(t, e.m.store, Person{Name: "Mom", Company: "eXp", Tags: []string{"family", "real estate"},
		CadenceDays: 7, LastContact: "2026-11-01", Birthday: "11-20"})
	add(t, e.m.store, Person{Name: "Steve", Company: "UMMS", Tags: []string{"healthcare"}})

	_, page := e.get(t, "/m/people")
	for _, want := range []string{"Reach out", "8 days over", "Birthdays", "Fri, Nov 20",
		"family", "real estate", "healthcare", "I reached out"} {
		if !strings.Contains(page, want) {
			t.Errorf("list page is missing %q", want)
		}
	}
	// Group by tag lists Mom under both of her buckets.
	if c := strings.Count(page, ">Mom</a>"); c < 3 { // reach-out + two buckets
		t.Errorf("Mom should appear under each bucket; found %d times", c)
	}

	_, page = e.get(t, "/m/people?group=company")
	if !strings.Contains(page, "eXp") || !strings.Contains(page, "UMMS") {
		t.Error("grouping by company lost the companies")
	}
	_, page = e.get(t, "/m/people?q=umms")
	if strings.Contains(page, ">Mom</a>") || !strings.Contains(page, "Steve") {
		t.Error("the search filter should keep Steve and drop Mom")
	}
}

func TestConnectionsThroughTheForms(t *testing.T) {
	e := newEnv(t)
	a := add(t, e.m.store, Person{Name: "Quincy"})
	b := add(t, e.m.store, Person{Name: "Will"})

	rec := e.post(t, "/m/people/connect", url.Values{
		"id": {a.ID}, "other": {b.ID}, "label": {"trades crew"},
	})
	if rec.Code != http.StatusSeeOther || len(e.m.store.Connections()) != 1 {
		t.Fatalf("connect: %d", rec.Code)
	}
	_, page := e.get(t, "/m/people/p?id="+a.ID)
	if !strings.Contains(page, "Will") || !strings.Contains(page, "trades crew") {
		t.Error("the detail page should show the connection")
	}
	rec = e.post(t, "/m/people/disconnect", url.Values{
		"id": {a.ID}, "conn": {e.m.store.Connections()[0].ID},
	})
	if rec.Code != http.StatusSeeOther || len(e.m.store.Connections()) != 0 {
		t.Error("disconnect failed")
	}
}

func TestGalaxyPageEmbedsTheGraph(t *testing.T) {
	e := newEnv(t)
	a := add(t, e.m.store, Person{Name: "Kwame", Tags: []string{"tech"}})
	b := add(t, e.m.store, Person{Name: "Steve", Tags: []string{"healthcare"}})
	if _, err := e.m.store.Connect(a.ID, b.ID, "intro", "you"); err != nil {
		t.Fatal(err)
	}

	code, page := e.get(t, "/m/people/galaxy")
	if code != 200 {
		t.Fatalf("galaxy: %d", code)
	}
	for _, want := range []string{`"nodes":`, `"Kwame"`, `"kind":"link"`, "canvas", "Suggest connections"} {
		if !strings.Contains(page, want) {
			t.Errorf("galaxy page is missing %q", want)
		}
	}
	// The filter drops the other bucket.
	_, page = e.get(t, "/m/people/galaxy?tag=tech")
	if strings.Contains(page, `"Steve"`) {
		t.Error("the tech filter should drop Steve")
	}
}

func TestWidgetsAndNav(t *testing.T) {
	e := newEnv(t)
	ws, err := e.m.Widgets(context.Background())
	if err != nil || len(ws) != 0 {
		t.Fatalf("an empty book should show no card: %v %v", ws, err)
	}
	add(t, e.m.store, Person{Name: "Mom", CadenceDays: 7, LastContact: "2026-11-01", Birthday: "11-18"})
	if e.m.Nav().Status != "warn" {
		t.Error("a birthday within 7 days should turn the nav amber")
	}
	ws, _ = e.m.Widgets(context.Background())
	if len(ws) != 1 || ws[0].Badge != "1 to reach out" || !ws[0].Warn {
		t.Fatalf("widget = %+v", ws)
	}
	if len(ws[0].Rows) != 2 || !strings.Contains(ws[0].Rows[0].Name, "🎂") {
		t.Errorf("rows = %+v", ws[0].Rows)
	}
}

func TestBirthdaysShowOnTheCalendar(t *testing.T) {
	e := newEnv(t)
	add(t, e.m.store, Person{Name: "Harmony", Birthday: "08-22"})
	evs, err := e.m.EventsOn(context.Background(), time.Date(2027, 8, 22, 0, 0, 0, 0, nyc))
	if err != nil || len(evs) != 1 {
		t.Fatalf("events = %v, %v", evs, err)
	}
	ev := evs[0]
	if !ev.AllDay || ev.Title != "🎂 Harmony" || ev.Source != "people" {
		t.Errorf("event = %+v", ev)
	}
	evs, _ = e.m.EventsOn(context.Background(), time.Date(2027, 8, 23, 0, 0, 0, 0, nyc))
	if len(evs) != 0 {
		t.Error("the day after should have no birthday")
	}
}

func TestSuggestConfirmApply(t *testing.T) {
	e := newEnv(t)
	a := add(t, e.m.store, Person{Name: "Quincy", Tags: []string{"trades"}, Notes: "SECRET-NOTE"})
	b := add(t, e.m.store, Person{Name: "Will", Tags: []string{"trades"}, Phone: "443-555-0000"})
	c := add(t, e.m.store, Person{Name: "Dad", Tags: []string{"family"}})

	e.ai.reply = fmt.Sprintf(
		`{"say":"Two fit.","links":[{"a":"%s","b":"%s","label":"both in the trades"},{"a":"%s","b":"%s","label":"family"}]}`,
		a.ID, b.ID, a.ID, c.ID)

	rec := e.post(t, "/m/people/suggest", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("suggest: %d %s", rec.Code, rec.Body.String())
	}
	page := rec.Body.String()
	if !strings.Contains(page, "both in the trades") || !strings.Contains(page, "Two fit.") {
		t.Fatalf("confirm page: %s", page)
	}
	// Privacy: notes and contact details never reach the prompt.
	if strings.Contains(e.ai.prompt, "SECRET-NOTE") || strings.Contains(e.ai.prompt, "443-555") {
		t.Fatal("notes or contact details leaked into the AI prompt")
	}
	if len(e.m.store.Connections()) != 0 {
		t.Fatal("nothing may be saved before the confirm")
	}

	token := extractToken(t, page)
	// Keep only the first suggestion.
	rec = e.post(t, "/m/people/suggest/apply", url.Values{"token": {token}, "keep": {"0"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("apply: %d", rec.Code)
	}
	conns := e.m.store.Connections()
	if len(conns) != 1 || conns[0].Label != "both in the trades" || conns[0].Source != "ai" {
		t.Fatalf("saved = %+v", conns)
	}

	// A token is single-use.
	rec = e.post(t, "/m/people/suggest/apply", url.Values{"token": {token}, "keep": {"1"}})
	if rec.Code != http.StatusGone {
		t.Errorf("a reused token should be refused: %d", rec.Code)
	}
}

func TestSuggestRejectsBadReplies(t *testing.T) {
	e := newEnv(t)
	a := add(t, e.m.store, Person{Name: "Quincy"})

	for _, links := range []string{
		`[{"a":"` + a.ID + `","b":"ghost","label":"?"}]`,        // unknown second person
		`[{"a":"ghost","b":"` + a.ID + `","label":"?"}]`,        // unknown first person
		`[{"a":"` + a.ID + `","b":"` + a.ID + `","label":"?"}]`, // self link
	} {
		e.ai.reply = `{"say":"x","links":` + links + `}`
		rec := e.post(t, "/m/people/suggest", nil)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s must be refused: %d %s", links, rec.Code, rec.Body.String())
		}
	}

	e.ai.reply = "I think the answer is yes!"
	rec := e.post(t, "/m/people/suggest", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a reply with no JSON must be refused: %d", rec.Code)
	}

	e.ai.err = errors.New("boom")
	e.ai.reply = ""
	rec = e.post(t, "/m/people/suggest", nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("an AI error should show as a failure: %d", rec.Code)
	}
}

func TestSuggestDropsAlreadyLinkedPairs(t *testing.T) {
	e := newEnv(t)
	a := add(t, e.m.store, Person{Name: "Quincy"})
	b := add(t, e.m.store, Person{Name: "Will"})
	if _, err := e.m.store.Connect(a.ID, b.ID, "already", "you"); err != nil {
		t.Fatal(err)
	}
	e.ai.reply = fmt.Sprintf(`{"say":"","links":[{"a":"%s","b":"%s","label":"dup"}]}`, b.ID, a.ID)
	rec := e.post(t, "/m/people/suggest", nil)
	// Everything suggested was a duplicate, so it redirects with a notice.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("all-duplicates: %d %s", rec.Code, rec.Body.String())
	}
	if len(e.m.store.Connections()) != 1 {
		t.Error("the duplicate must not be saved twice")
	}
}

func extractToken(t *testing.T, page string) string {
	t.Helper()
	marker := `name="token" value="`
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatal("no token on the confirm page")
	}
	rest := page[i+len(marker):]
	return rest[:strings.Index(rest, `"`)]
}
