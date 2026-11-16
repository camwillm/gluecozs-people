// Package people is the Gluecozs people book: everyone worth staying in
// touch with, which buckets they belong to, why they matter in each, how
// often to reach out, and how they connect to each other. The galaxy page
// draws those connections as a map.
package people

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/camwillm/gluecozs-sdk"
)

// Module is what the core registers.
type Module struct {
	store *Store
	loc   *time.Location
	host  sdk.Host
	pend  *pendingStore
	now   func() time.Time
}

// New builds the people module.
func New(store *Store, loc *time.Location) *Module {
	return &Module{store: store, loc: loc, pend: newPendingStore(), now: time.Now}
}

func (m *Module) ID() string { return "people" }

// Store gives seeding and tests access to the book.
func (m *Module) Store() *Store { return m.store }

// Nav is the entry in the molecule nav. It turns amber when a birthday is
// within the next 7 days, so none slips by.
func (m *Module) Nav() sdk.NavItem {
	status := "ok"
	if len(m.store.Birthdays(m.now().In(m.loc), 7)) > 0 {
		status = "warn"
	}
	return sdk.NavItem{
		ID:    "people",
		Label: "People",
		Href:  "/m/people",
		Icon: `<circle cx="9" cy="8" r="3.2"/><path d="M3.5 20c0-3 2.5-5.5 5.5-5.5S14.5 17 14.5 20"/>` +
			`<circle cx="17" cy="9" r="2.4"/><path d="M16 14.6c2.6.5 4.5 2.7 4.5 5.4"/>`,
		Status: status,
	}
}

// EventsOn implements sdk.EventProvider: birthdays show up on the calendar
// day view as all-day events.
func (m *Module) EventsOn(ctx context.Context, day time.Time) ([]sdk.Event, error) {
	y, mo, d := day.Date()
	start := time.Date(y, mo, d, 0, 0, 0, 0, m.loc)
	var out []sdk.Event
	for _, p := range m.store.BirthdaysOn(ctx, start) {
		out = append(out, sdk.Event{
			ID:       "people:" + p.ID + ":" + start.Format(dateLayout),
			Title:    "🎂 " + p.Name,
			Tag:      "Birthday",
			Source:   "people",
			Calendar: "people",
			AllDay:   true,
			Start:    start,
			End:      start.AddDate(0, 0, 1),
		})
	}
	return out, nil
}

// Widgets implements sdk.WidgetProvider: the Focus card shows who to reach
// out to and whose birthday is close. It only reads memory, so it is quick.
func (m *Module) Widgets(context.Context) ([]sdk.Widget, error) {
	now := m.now().In(m.loc)
	due := m.store.Due(now)
	bdays := m.store.Birthdays(now, 7)
	if len(due) == 0 && len(bdays) == 0 {
		return nil, nil
	}

	w := sdk.Widget{Title: "People"}
	if len(due) > 0 {
		w.Badge = fmt.Sprintf("%d to reach out", len(due))
	}
	w.Warn = len(bdays) > 0

	for i, b := range bdays {
		if i == 2 {
			break
		}
		when := "today"
		if b.InDays == 1 {
			when = "tomorrow"
		} else if b.InDays > 1 {
			when = b.Date.Format("Mon Jan 2")
		}
		w.Rows = append(w.Rows, sdk.Row{
			Name: "🎂 " + b.Person.Name, Value: when, Percent: 100, Warn: true,
		})
	}
	for i, d := range due {
		if i == 3 {
			break
		}
		pct := 100
		if d.Person.CadenceDays > 0 {
			pct = min(100, d.OverdueDays*100/d.Person.CadenceDays)
		}
		label := "due today"
		if d.OverdueDays == 1 {
			label = "1 day over"
		} else if d.OverdueDays > 1 {
			label = fmt.Sprintf("%d days over", d.OverdueDays)
		}
		w.Rows = append(w.Rows, sdk.Row{Name: d.Person.Name, Value: label, Percent: pct})
	}
	return []sdk.Widget{w}, nil
}

// Mount registers the module's pages and endpoints.
func (m *Module) Mount(mux *http.ServeMux, host sdk.Host) {
	m.host = host

	mux.HandleFunc("GET /m/people", m.listPage)
	mux.HandleFunc("GET /m/people/new", m.newPage)
	mux.HandleFunc("POST /m/people/new", m.create)
	mux.HandleFunc("GET /m/people/p", m.detailPage)
	mux.HandleFunc("GET /m/people/edit", m.editPage)
	mux.HandleFunc("POST /m/people/update", m.update)
	mux.HandleFunc("POST /m/people/delete", m.remove)
	mux.HandleFunc("POST /m/people/touch", m.touch)
	mux.HandleFunc("POST /m/people/connect", m.connect)
	mux.HandleFunc("POST /m/people/disconnect", m.disconnect)
	mux.HandleFunc("GET /m/people/galaxy", m.galaxyPage)
	mux.HandleFunc("POST /m/people/suggest", m.suggest)
	mux.HandleFunc("POST /m/people/suggest/apply", m.applySuggestions)
	mux.HandleFunc("POST /m/people/suggest/discard", m.discardSuggestions)
}

var (
	_ sdk.Module         = (*Module)(nil)
	_ sdk.EventProvider  = (*Module)(nil)
	_ sdk.WidgetProvider = (*Module)(nil)
	_ sdk.Mounter        = (*Module)(nil)
)
