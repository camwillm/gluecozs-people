package people

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const dateLayout = "2006-01-02"

// Person is one entry in the book. Tags are the buckets they belong to
// (family, tech, real estate, vibes, ...), and Why holds one line per tag
// saying why they matter in that bucket.
type Person struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Phone   string `json:"phone,omitempty"`
	Email   string `json:"email,omitempty"`
	Company string `json:"company,omitempty"`
	City    string `json:"city,omitempty"`
	// Birthday is "YYYY-MM-DD" or "MM-DD"; the year is optional.
	Birthday string            `json:"birthday,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
	Why      map[string]string `json:"why,omitempty"`
	HowMet   string            `json:"how_met,omitempty"`
	Notes    string            `json:"notes,omitempty"`
	// Pipeline is a stage for career and client contacts:
	// "", "to reach out", "chatted", "followed up", "referred / hired".
	Pipeline string `json:"pipeline,omitempty"`
	// CadenceDays is how often to be in touch. 0 means no reminder.
	CadenceDays int    `json:"cadence_days,omitempty"`
	LastContact string `json:"last_contact,omitempty"` // YYYY-MM-DD
	Archived    bool   `json:"archived,omitempty"`
	Created     string `json:"created,omitempty"` // YYYY-MM-DD
}

// Interaction is one logged touch with a person.
type Interaction struct {
	ID       string `json:"id"`
	PersonID string `json:"person_id"`
	Date     string `json:"date"` // YYYY-MM-DD
	Kind     string `json:"kind"` // text, call, met, event, other
	Note     string `json:"note,omitempty"`
}

// Connection is one labelled edge between two people. A and B are person
// ids, stored with A < B so each pair exists once.
type Connection struct {
	ID     string `json:"id"`
	A      string `json:"a"`
	B      string `json:"b"`
	Label  string `json:"label,omitempty"`
	Source string `json:"source,omitempty"` // "you" or "ai"
}

type state struct {
	NextPerson   int           `json:"next_person"`
	NextTouch    int           `json:"next_touch"`
	NextConn     int           `json:"next_conn"`
	People       []Person      `json:"people"`
	Interactions []Interaction `json:"interactions"`
	Connections  []Connection  `json:"connections"`
}

func (st state) clone() state {
	next := st
	next.People = append([]Person(nil), st.People...)
	for i, p := range next.People {
		next.People[i].Tags = append([]string(nil), p.Tags...)
		if p.Why != nil {
			w := make(map[string]string, len(p.Why))
			for k, v := range p.Why {
				w[k] = v
			}
			next.People[i].Why = w
		}
	}
	next.Interactions = append([]Interaction(nil), st.Interactions...)
	next.Connections = append([]Connection(nil), st.Connections...)
	return next
}

// Store keeps everything in memory and mirrors it to one JSON file with an
// atomic write, the same way the calendar does.
type Store struct {
	mu   sync.RWMutex
	st   state
	path string
}

func NewStore() *Store { return &Store{} }

// OpenStore reads the file if it exists and creates it on the first save.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s *Store) save(st state) error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".people-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func (s *Store) mutate(change func(st *state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.st.clone()
	if err := change(&next); err != nil {
		return err
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.st = next
	return nil
}

func normalizePerson(p Person) (Person, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, errors.New("a person needs a name")
	}
	p.Company = strings.TrimSpace(p.Company)
	p.City = strings.TrimSpace(p.City)
	p.Birthday = strings.TrimSpace(p.Birthday)
	if p.Birthday != "" {
		if _, _, ok := birthdayMonthDay(p.Birthday); !ok {
			return p, fmt.Errorf("birthday %q should be YYYY-MM-DD or MM-DD", p.Birthday)
		}
	}
	if p.LastContact != "" {
		if _, err := time.Parse(dateLayout, p.LastContact); err != nil {
			return p, fmt.Errorf("last contact %q should be YYYY-MM-DD", p.LastContact)
		}
	}
	if p.CadenceDays < 0 {
		p.CadenceDays = 0
	}
	tags := make([]string, 0, len(p.Tags))
	seen := map[string]bool{}
	for _, t := range p.Tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
	}
	p.Tags = tags
	return p, nil
}

// birthdayMonthDay reads "YYYY-MM-DD" or "MM-DD".
func birthdayMonthDay(s string) (month time.Month, day int, ok bool) {
	if t, err := time.Parse(dateLayout, s); err == nil {
		return t.Month(), t.Day(), true
	}
	if t, err := time.Parse("01-02", s); err == nil {
		return t.Month(), t.Day(), true
	}
	return 0, 0, false
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.st.People)
}

func (s *Store) Add(p Person, now time.Time) (Person, error) {
	p, err := normalizePerson(p)
	if err != nil {
		return Person{}, err
	}
	err = s.mutate(func(st *state) error {
		st.NextPerson++
		p.ID = fmt.Sprintf("p%d", st.NextPerson)
		if p.Created == "" {
			p.Created = now.Format(dateLayout)
		}
		st.People = append(st.People, p)
		return nil
	})
	return p, err
}

func (s *Store) Get(id string) (Person, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.st.People {
		if p.ID == id {
			return p, true
		}
	}
	return Person{}, false
}

func (s *Store) Update(p Person) error {
	p, err := normalizePerson(p)
	if err != nil {
		return err
	}
	return s.mutate(func(st *state) error {
		for i := range st.People {
			if st.People[i].ID == p.ID {
				if p.Created == "" {
					p.Created = st.People[i].Created
				}
				st.People[i] = p
				return nil
			}
		}
		return fmt.Errorf("no person %s", p.ID)
	})
}

// Delete removes a person and everything that points at them.
func (s *Store) Delete(id string) error {
	return s.mutate(func(st *state) error {
		idx := -1
		for i := range st.People {
			if st.People[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("no person %s", id)
		}
		st.People = append(st.People[:idx], st.People[idx+1:]...)
		keepT := st.Interactions[:0]
		for _, t := range st.Interactions {
			if t.PersonID != id {
				keepT = append(keepT, t)
			}
		}
		st.Interactions = keepT
		keepC := st.Connections[:0]
		for _, c := range st.Connections {
			if c.A != id && c.B != id {
				keepC = append(keepC, c)
			}
		}
		st.Connections = keepC
		return nil
	})
}

// All returns everyone, active people first, each list sorted by name.
func (s *Store) All() []Person {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Person(nil), s.st.People...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Archived != out[j].Archived {
			return !out[i].Archived
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// LogTouch records an interaction and moves the person's last-contact date
// forward (never backward).
func (s *Store) LogTouch(t Interaction) (Interaction, error) {
	t.Kind = strings.TrimSpace(t.Kind)
	if t.Kind == "" {
		t.Kind = "text"
	}
	if t.Date == "" {
		return Interaction{}, errors.New("an interaction needs a date")
	}
	if _, err := time.Parse(dateLayout, t.Date); err != nil {
		return Interaction{}, fmt.Errorf("date %q should be YYYY-MM-DD", t.Date)
	}
	err := s.mutate(func(st *state) error {
		found := false
		for i := range st.People {
			if st.People[i].ID == t.PersonID {
				found = true
				if st.People[i].LastContact < t.Date {
					st.People[i].LastContact = t.Date
				}
			}
		}
		if !found {
			return fmt.Errorf("no person %s", t.PersonID)
		}
		st.NextTouch++
		t.ID = fmt.Sprintf("t%d", st.NextTouch)
		st.Interactions = append(st.Interactions, t)
		return nil
	})
	return t, err
}

// Touches returns a person's interactions, newest first.
func (s *Store) Touches(personID string) []Interaction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Interaction
	for _, t := range s.st.Interactions {
		if t.PersonID == personID {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out
}

func connKey(a, b string) (string, string) {
	if b < a {
		a, b = b, a
	}
	return a, b
}

// Connect links two people. Linking an already linked pair updates the label.
func (s *Store) Connect(a, b, label, source string) (Connection, error) {
	if a == b {
		return Connection{}, errors.New("a person cannot connect to themselves")
	}
	a, b = connKey(a, b)
	var out Connection
	err := s.mutate(func(st *state) error {
		var haveA, haveB bool
		for _, p := range st.People {
			haveA = haveA || p.ID == a
			haveB = haveB || p.ID == b
		}
		if !haveA || !haveB {
			return errors.New("both people must exist")
		}
		for i := range st.Connections {
			if st.Connections[i].A == a && st.Connections[i].B == b {
				st.Connections[i].Label = strings.TrimSpace(label)
				st.Connections[i].Source = source
				out = st.Connections[i]
				return nil
			}
		}
		st.NextConn++
		out = Connection{
			ID: fmt.Sprintf("c%d", st.NextConn), A: a, B: b,
			Label: strings.TrimSpace(label), Source: source,
		}
		st.Connections = append(st.Connections, out)
		return nil
	})
	return out, err
}

func (s *Store) Disconnect(id string) error {
	return s.mutate(func(st *state) error {
		for i := range st.Connections {
			if st.Connections[i].ID == id {
				st.Connections = append(st.Connections[:i], st.Connections[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("no connection %s", id)
	})
}

func (s *Store) Connections() []Connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Connection(nil), s.st.Connections...)
}

// ConnectionsOf returns a person's connections together with the person on
// the other end.
func (s *Store) ConnectionsOf(id string) []struct {
	Conn  Connection
	Other Person
} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []struct {
		Conn  Connection
		Other Person
	}
	for _, c := range s.st.Connections {
		otherID := ""
		switch id {
		case c.A:
			otherID = c.B
		case c.B:
			otherID = c.A
		default:
			continue
		}
		for _, p := range s.st.People {
			if p.ID == otherID {
				out = append(out, struct {
					Conn  Connection
					Other Person
				}{c, p})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Other.Name) < strings.ToLower(out[j].Other.Name)
	})
	return out
}

// DuePerson is someone past their cadence.
type DuePerson struct {
	Person      Person
	OverdueDays int
}

// Due lists everyone whose next touch is on or before today, most overdue
// first. People with no cadence, and archived people, are never due.
func (s *Store) Due(now time.Time) []DuePerson {
	s.mu.RLock()
	defer s.mu.RUnlock()
	today := now.Format(dateLayout)
	var out []DuePerson
	for _, p := range s.st.People {
		if p.Archived || p.CadenceDays <= 0 {
			continue
		}
		anchor := p.LastContact
		if anchor == "" {
			anchor = p.Created
		}
		if anchor == "" {
			anchor = today
		}
		at, err := time.Parse(dateLayout, anchor)
		if err != nil {
			continue
		}
		due := at.AddDate(0, 0, p.CadenceDays)
		nowD, _ := time.Parse(dateLayout, today)
		if due.After(nowD) {
			continue
		}
		out = append(out, DuePerson{Person: p, OverdueDays: int(nowD.Sub(due).Hours() / 24)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OverdueDays != out[j].OverdueDays {
			return out[i].OverdueDays > out[j].OverdueDays
		}
		return strings.ToLower(out[i].Person.Name) < strings.ToLower(out[j].Person.Name)
	})
	return out
}

// UpcomingBirthday is a birthday inside the asked-for window.
type UpcomingBirthday struct {
	Person Person
	Date   time.Time // this year's (or next year's) occurrence
	InDays int
}

// Birthdays lists birthdays in the next `days` days, soonest first, today
// included. It walks day by day, which also handles the year wrap.
func (s *Store) Birthdays(now time.Time, days int) []UpcomingBirthday {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []UpcomingBirthday
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for _, p := range s.st.People {
		if p.Archived {
			continue
		}
		m, d, ok := birthdayMonthDay(p.Birthday)
		if !ok {
			continue
		}
		for off := 0; off <= days; off++ {
			day := start.AddDate(0, 0, off)
			if day.Month() == m && day.Day() == d {
				out = append(out, UpcomingBirthday{Person: p, Date: day, InDays: off})
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].InDays != out[j].InDays {
			return out[i].InDays < out[j].InDays
		}
		return strings.ToLower(out[i].Person.Name) < strings.ToLower(out[j].Person.Name)
	})
	return out
}

// BirthdaysOn returns the people whose birthday falls on the given day.
func (s *Store) BirthdaysOn(ctx context.Context, day time.Time) []Person {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Person
	for _, p := range s.st.People {
		if p.Archived {
			continue
		}
		if m, d, ok := birthdayMonthDay(p.Birthday); ok && m == day.Month() && d == day.Day() {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
