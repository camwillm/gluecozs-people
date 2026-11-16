package people

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/people.html
var tplFS embed.FS

var tpl = template.Must(template.ParseFS(tplFS, "templates/people.html"))

// ---- view data ----------------------------------------------------------

type personRow struct {
	ID, Name, Company, Meta string
	Tags                    []string
	Pipeline                string
	OverdueText             string // "" when not overdue
}

type group struct {
	Label string
	Rows  []personRow
}

type bdayRow struct {
	ID, Name, When string
}

type connRow struct {
	ID, OtherID, OtherName, Label, Source string
}

type touchRow struct {
	Date, Kind, Note string
}

type formValues struct {
	ID, Name, Phone, Email, Company, City string
	Birthday, Cadence, LastContact        string
	Tags, Why, HowMet, Notes, Pipeline    string
	Archived                              bool
}

type whyRow struct{ Tag, Text string }

type pageData struct {
	Error, Notice string
	AIReady       bool

	// list page
	Due     []personRow
	Bdays   []bdayRow
	Groups  []group
	GroupBy string
	Query   string
	TagOpts []string
	Total   int

	// form pages
	Form  formValues
	IsNew bool

	// detail page
	Person    Person
	WhyRows   []whyRow
	Conns     []connRow
	Touches   []touchRow
	Others    []Person // for the connect form
	Overdue   string
	BirthdayT string

	// galaxy page
	GraphJSON template.JS
	TagFilter string

	// confirm page
	Token       string
	Say         string
	Suggestions []connRow
}

func (m *Module) render(w http.ResponseWriter, r *http.Request, status int, name, title string, data pageData) {
	if m.host != nil && m.host.AI() != nil {
		data.AIReady = m.host.AI().Available()
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("people: render %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	m.host.Page(w, r, status, "people", title, template.HTML(buf.String()))
}

// ---- list ---------------------------------------------------------------

func overdueText(d DuePerson) string {
	switch {
	case d.OverdueDays <= 0:
		return "due today"
	case d.OverdueDays == 1:
		return "1 day over"
	default:
		return fmt.Sprintf("%d days over", d.OverdueDays)
	}
}

func rowFor(p Person, over map[string]string) personRow {
	meta := p.Company
	if p.City != "" {
		if meta != "" {
			meta += " · "
		}
		meta += p.City
	}
	return personRow{
		ID: p.ID, Name: p.Name, Company: p.Company, Meta: meta,
		Tags: p.Tags, Pipeline: p.Pipeline, OverdueText: over[p.ID],
	}
}

func matches(p Person, q string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	hay := strings.ToLower(p.Name + " " + p.Company + " " + p.City + " " + strings.Join(p.Tags, " "))
	return strings.Contains(hay, q)
}

func (m *Module) listPage(w http.ResponseWriter, r *http.Request) {
	now := m.now().In(m.loc)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	groupBy := r.URL.Query().Get("group")
	if groupBy == "" {
		groupBy = "tag"
	}

	due := m.store.Due(now)
	over := map[string]string{}
	data := pageData{GroupBy: groupBy, Query: q,
		Notice: r.URL.Query().Get("notice"), Error: r.URL.Query().Get("err")}
	for _, d := range due {
		over[d.Person.ID] = overdueText(d)
		if matches(d.Person, q) {
			data.Due = append(data.Due, rowFor(d.Person, over))
		}
	}
	for _, b := range m.store.Birthdays(now, 30) {
		if !matches(b.Person, q) {
			continue
		}
		when := "today 🎉"
		if b.InDays == 1 {
			when = "tomorrow"
		} else if b.InDays > 1 {
			when = b.Date.Format("Mon, Jan 2")
		}
		data.Bdays = append(data.Bdays, bdayRow{ID: b.Person.ID, Name: b.Person.Name, When: when})
	}

	all := m.store.All()
	tagSeen := map[string]bool{}
	for _, p := range all {
		for _, t := range p.Tags {
			if !tagSeen[strings.ToLower(t)] {
				tagSeen[strings.ToLower(t)] = true
				data.TagOpts = append(data.TagOpts, t)
			}
		}
	}
	sort.Strings(data.TagOpts)

	buckets := map[string][]personRow{}
	var order []string
	add := func(label string, p Person) {
		if _, ok := buckets[label]; !ok {
			order = append(order, label)
		}
		buckets[label] = append(buckets[label], rowFor(p, over))
	}
	for _, p := range all {
		if !matches(p, q) {
			continue
		}
		data.Total++
		label := ""
		switch {
		case p.Archived:
			label = "Archived"
		case groupBy == "company":
			label = p.Company
			if label == "" {
				label = "No company"
			}
		case groupBy == "city":
			label = p.City
			if label == "" {
				label = "No city"
			}
		case groupBy == "none":
			label = "Everyone"
		default: // tag: a person appears under each of their buckets
			if len(p.Tags) == 0 {
				add("No bucket yet", p)
				continue
			}
			for _, t := range p.Tags {
				add(t, p)
			}
			continue
		}
		add(label, p)
	}
	sort.SliceStable(order, func(i, j int) bool {
		// Archived last, the rest alphabetically.
		ai, aj := order[i] == "Archived", order[j] == "Archived"
		if ai != aj {
			return aj
		}
		return strings.ToLower(order[i]) < strings.ToLower(order[j])
	})
	for _, label := range order {
		data.Groups = append(data.Groups, group{Label: label, Rows: buckets[label]})
	}

	m.render(w, r, http.StatusOK, "list", "People", data)
}

// ---- add and edit -------------------------------------------------------

func formFrom(p Person) formValues {
	var why []string
	var tags []string
	for _, t := range p.Tags {
		tags = append(tags, t)
		if line := p.Why[strings.ToLower(t)]; line != "" {
			why = append(why, t+": "+line)
		}
	}
	cad := ""
	if p.CadenceDays > 0 {
		cad = strconv.Itoa(p.CadenceDays)
	}
	return formValues{
		ID: p.ID, Name: p.Name, Phone: p.Phone, Email: p.Email,
		Company: p.Company, City: p.City, Birthday: p.Birthday,
		Cadence: cad, LastContact: p.LastContact,
		Tags: strings.Join(tags, ", "), Why: strings.Join(why, "\n"),
		HowMet: p.HowMet, Notes: p.Notes, Pipeline: p.Pipeline, Archived: p.Archived,
	}
}

func personFrom(r *http.Request) (Person, error) {
	cadence := 0
	if c := strings.TrimSpace(r.FormValue("cadence")); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil || n < 0 || n > 3650 {
			return Person{}, fmt.Errorf("cadence %q should be a number of days", c)
		}
		cadence = n
	}
	var tags []string
	for _, t := range strings.Split(r.FormValue("tags"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	// Why lines are "tag: why they matter there", one per line.
	why := map[string]string{}
	for _, line := range strings.Split(r.FormValue("why"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		tag, text, ok := strings.Cut(line, ":")
		if !ok {
			return Person{}, fmt.Errorf("the line %q should look like \"tag: why they matter\"", line)
		}
		why[strings.ToLower(strings.TrimSpace(tag))] = strings.TrimSpace(text)
	}
	return Person{
		ID:       r.FormValue("id"),
		Name:     r.FormValue("name"),
		Phone:    strings.TrimSpace(r.FormValue("phone")),
		Email:    strings.TrimSpace(r.FormValue("email")),
		Company:  r.FormValue("company"),
		City:     r.FormValue("city"),
		Birthday: r.FormValue("birthday"),
		Tags:     tags, Why: why,
		HowMet:   strings.TrimSpace(r.FormValue("how_met")),
		Notes:    strings.TrimSpace(r.FormValue("notes")),
		Pipeline: r.FormValue("pipeline"), CadenceDays: cadence,
		LastContact: strings.TrimSpace(r.FormValue("last_contact")),
		Archived:    r.FormValue("archived") != "",
	}, nil
}

func (m *Module) newPage(w http.ResponseWriter, r *http.Request) {
	m.render(w, r, http.StatusOK, "form", "Add a person", pageData{IsNew: true, Form: formValues{Cadence: ""}})
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, err := personFrom(r)
	if err == nil {
		p, err = m.store.Add(p, m.now().In(m.loc))
	}
	if err != nil {
		m.render(w, r, http.StatusUnprocessableEntity, "form", "Add a person",
			pageData{IsNew: true, Form: formFrom(p), Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/m/people/p?id="+url.QueryEscape(p.ID), http.StatusSeeOther)
}

func (m *Module) editPage(w http.ResponseWriter, r *http.Request) {
	p, ok := m.store.Get(r.URL.Query().Get("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	m.render(w, r, http.StatusOK, "form", "Edit "+p.Name, pageData{Form: formFrom(p)})
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) {
	p, err := personFrom(r)
	if err == nil {
		err = m.store.Update(p)
	}
	if err != nil {
		m.render(w, r, http.StatusUnprocessableEntity, "form", "Edit",
			pageData{Form: formFrom(p), Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/m/people/p?id="+url.QueryEscape(p.ID), http.StatusSeeOther)
}

func (m *Module) remove(w http.ResponseWriter, r *http.Request) {
	if err := m.store.Delete(r.FormValue("id")); err != nil {
		http.Redirect(w, r, "/m/people?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/m/people?notice="+url.QueryEscape("Removed."), http.StatusSeeOther)
}

// ---- detail -------------------------------------------------------------

func (m *Module) detailPage(w http.ResponseWriter, r *http.Request) {
	p, ok := m.store.Get(r.URL.Query().Get("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	now := m.now().In(m.loc)
	data := pageData{Person: p, Notice: r.URL.Query().Get("notice"), Error: r.URL.Query().Get("err")}
	for _, t := range p.Tags {
		data.WhyRows = append(data.WhyRows, whyRow{Tag: t, Text: p.Why[strings.ToLower(t)]})
	}
	for _, d := range m.store.Due(now) {
		if d.Person.ID == p.ID {
			data.Overdue = overdueText(d)
		}
	}
	for _, b := range m.store.Birthdays(now, 30) {
		if b.Person.ID == p.ID {
			data.BirthdayT = b.Date.Format("Monday, Jan 2")
		}
	}
	for _, c := range m.store.ConnectionsOf(p.ID) {
		data.Conns = append(data.Conns, connRow{
			ID: c.Conn.ID, OtherID: c.Other.ID, OtherName: c.Other.Name,
			Label: c.Conn.Label, Source: c.Conn.Source,
		})
	}
	for _, t := range m.store.Touches(p.ID) {
		data.Touches = append(data.Touches, touchRow{Date: t.Date, Kind: t.Kind, Note: t.Note})
	}
	for _, o := range m.store.All() {
		if o.ID != p.ID && !o.Archived {
			data.Others = append(data.Others, o)
		}
	}
	m.render(w, r, http.StatusOK, "detail", p.Name, data)
}

// touch logs an interaction and bumps last contact.
func (m *Module) touch(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	date := strings.TrimSpace(r.FormValue("date"))
	if date == "" {
		date = m.now().In(m.loc).Format(dateLayout)
	}
	_, err := m.store.LogTouch(Interaction{
		PersonID: id, Date: date,
		Kind: r.FormValue("kind"), Note: strings.TrimSpace(r.FormValue("note")),
	})
	back := "/m/people/p?id=" + url.QueryEscape(id)
	if err != nil {
		http.Redirect(w, r, back+"&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if r.FormValue("from") == "list" {
		http.Redirect(w, r, "/m/people?notice="+url.QueryEscape("Logged."), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"&notice="+url.QueryEscape("Logged."), http.StatusSeeOther)
}

func (m *Module) connect(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	_, err := m.store.Connect(id, r.FormValue("other"), r.FormValue("label"), "you")
	back := "/m/people/p?id=" + url.QueryEscape(id)
	if err != nil {
		http.Redirect(w, r, back+"&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (m *Module) disconnect(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	if err := m.store.Disconnect(r.FormValue("conn")); err != nil {
		http.Redirect(w, r, "/m/people/p?id="+url.QueryEscape(id)+"&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/m/people/p?id="+url.QueryEscape(id), http.StatusSeeOther)
}

// ---- galaxy -------------------------------------------------------------

func (m *Module) galaxyPage(w http.ResponseWriter, r *http.Request) {
	now := m.now().In(m.loc)
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	g := BuildGraph(m.store, m.store.Due(now), tag)
	js, err := json.Marshal(g)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := pageData{GraphJSON: template.JS(js), TagFilter: tag,
		Notice: r.URL.Query().Get("notice")}
	for _, p := range m.store.All() {
		for _, t := range p.Tags {
			found := false
			for _, o := range data.TagOpts {
				found = found || strings.EqualFold(o, t)
			}
			if !found {
				data.TagOpts = append(data.TagOpts, t)
			}
		}
	}
	sort.Strings(data.TagOpts)
	m.render(w, r, http.StatusOK, "galaxy", "Galaxy", data)
}

// ---- AI suggestions -----------------------------------------------------

func (m *Module) suggest(w http.ResponseWriter, r *http.Request) {
	ai := m.host.AI()
	if ai == nil || !ai.Available() {
		http.Redirect(w, r, "/m/people/galaxy?tag=", http.StatusSeeOther)
		return
	}
	people := m.store.All()
	byID := map[string]Person{}
	for _, p := range people {
		byID[p.ID] = p
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	reply, err := ai.Ask(ctx, suggestSystem, buildSuggestPrompt(people, m.store.Connections(), byID))
	if err != nil {
		m.render(w, r, http.StatusBadGateway, "confirm", "Suggestions",
			pageData{Error: "The AI call failed: " + err.Error()})
		return
	}
	say, links, err := parseSuggestions(reply, m.store)
	if err != nil {
		m.render(w, r, http.StatusUnprocessableEntity, "confirm", "Suggestions",
			pageData{Error: err.Error()})
		return
	}
	if len(links) == 0 {
		msg := say
		if msg == "" {
			msg = "No new connections to suggest."
		}
		http.Redirect(w, r, "/m/people/galaxy?notice="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	data := pageData{Say: say, Token: m.pend.put(say, links, m.now())}
	for i, l := range links {
		data.Suggestions = append(data.Suggestions, connRow{
			ID: strconv.Itoa(i), OtherID: l.A.Name, OtherName: l.B.Name, Label: l.Label,
		})
	}
	m.render(w, r, http.StatusOK, "confirm", "Suggestions", data)
}

func (m *Module) applySuggestions(w http.ResponseWriter, r *http.Request) {
	_, links, ok := m.pend.take(r.FormValue("token"), m.now())
	if !ok {
		m.render(w, r, http.StatusGone, "confirm", "Suggestions",
			pageData{Error: "That suggestion list expired. Ask again."})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	chosen := map[string]bool{}
	for _, v := range r.PostForm["keep"] {
		chosen[v] = true
	}
	saved := 0
	for i, l := range links {
		if !chosen[strconv.Itoa(i)] {
			continue
		}
		if _, err := m.store.Connect(l.A.ID, l.B.ID, l.Label, "ai"); err == nil {
			saved++
		}
	}
	http.Redirect(w, r, "/m/people/galaxy?notice="+url.QueryEscape(fmt.Sprintf("Added %d connections.", saved)), http.StatusSeeOther)
}

func (m *Module) discardSuggestions(w http.ResponseWriter, r *http.Request) {
	m.pend.take(r.FormValue("token"), m.now())
	http.Redirect(w, r, "/m/people/galaxy", http.StatusSeeOther)
}
