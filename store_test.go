package people

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var nyc = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

// Monday, November 16, 2026.
var now = time.Date(2026, time.November, 16, 9, 0, 0, 0, nyc)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "people.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func add(t *testing.T, s *Store, p Person) Person {
	t.Helper()
	out, err := s.Add(p, now)
	if err != nil {
		t.Fatalf("add %s: %v", p.Name, err)
	}
	return out
}

func TestAddGetUpdateDelete(t *testing.T) {
	s := testStore(t)
	p := add(t, s, Person{Name: "  Kwame  ", Company: "BigCo", Tags: []string{"tech", " tech ", "vibes", ""},
		Why: map[string]string{"tech": "honest DS advice"}})
	if p.ID != "p1" || p.Name != "Kwame" {
		t.Fatalf("p = %+v", p)
	}
	if len(p.Tags) != 2 {
		t.Errorf("tags should be de-duplicated and trimmed: %v", p.Tags)
	}
	if p.Created != "2026-11-16" {
		t.Errorf("created = %q", p.Created)
	}

	p.City = "Baltimore"
	if err := s.Update(p); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("p1")
	if !ok || got.City != "Baltimore" || got.Created != "2026-11-16" {
		t.Errorf("after update: %+v", got)
	}

	if _, err := s.Add(Person{Name: ""}, now); err == nil {
		t.Error("a nameless person should be refused")
	}
	if _, err := s.Add(Person{Name: "X", Birthday: "Dec 29"}, now); err == nil {
		t.Error("a free-text birthday should be refused")
	}

	if err := s.Delete("p1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("p1"); ok {
		t.Error("p1 should be gone")
	}
}

func TestPersistenceSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "people.json")
	s, _ := OpenStore(path)
	a := add(t, s, Person{Name: "Mom", Tags: []string{"family", "real estate"}, Birthday: "1970-05-02"})
	b := add(t, s, Person{Name: "Harmony", Tags: []string{"family"}, Birthday: "08-22"})
	if _, err := s.Connect(a.ID, b.ID, "family", "you"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LogTouch(Interaction{PersonID: a.ID, Date: "2026-11-10", Kind: "call"}); err != nil {
		t.Fatal(err)
	}

	re, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if re.Len() != 2 || len(re.Connections()) != 1 || len(re.Touches(a.ID)) != 1 {
		t.Fatalf("reopened store lost data: %d people, %d conns, %d touches",
			re.Len(), len(re.Connections()), len(re.Touches(a.ID)))
	}
	got, _ := re.Get(a.ID)
	if got.LastContact != "2026-11-10" {
		t.Errorf("last contact = %q", got.LastContact)
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("people.json mode = %v, want 0600: it holds personal data", info.Mode().Perm())
	}
}

func TestDue(t *testing.T) {
	s := testStore(t)
	add(t, s, Person{Name: "Fresh", CadenceDays: 30, LastContact: "2026-11-10"})           // due Dec 10
	over := add(t, s, Person{Name: "Overdue", CadenceDays: 14, LastContact: "2026-10-26"}) // due Nov 9: 7 over
	most := add(t, s, Person{Name: "Way over", CadenceDays: 7, LastContact: "2026-10-01"}) // 39 over
	add(t, s, Person{Name: "No cadence"})
	arch := add(t, s, Person{Name: "Archived", CadenceDays: 7, LastContact: "2026-01-01"})
	arch.Archived = true
	if err := s.Update(arch); err != nil {
		t.Fatal(err)
	}
	never := add(t, s, Person{Name: "Never contacted", CadenceDays: 7}) // anchored to created (today)
	_ = never

	due := s.Due(now)
	if len(due) != 2 {
		t.Fatalf("due = %d people, want 2: %+v", len(due), due)
	}
	if due[0].Person.ID != most.ID || due[0].OverdueDays != 39 {
		t.Errorf("most overdue first: got %s (%d days)", due[0].Person.Name, due[0].OverdueDays)
	}
	if due[1].Person.ID != over.ID || due[1].OverdueDays != 7 {
		t.Errorf("second: got %s (%d days)", due[1].Person.Name, due[1].OverdueDays)
	}
}

func TestLogTouchMovesLastContactForwardOnly(t *testing.T) {
	s := testStore(t)
	p := add(t, s, Person{Name: "Steve", LastContact: "2026-11-10"})
	if _, err := s.LogTouch(Interaction{PersonID: p.ID, Date: "2026-11-01", Kind: "text"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(p.ID)
	if got.LastContact != "2026-11-10" {
		t.Errorf("a back-dated touch moved last contact backward: %q", got.LastContact)
	}
	if _, err := s.LogTouch(Interaction{PersonID: p.ID, Date: "2026-11-15", Kind: "call"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(p.ID)
	if got.LastContact != "2026-11-15" {
		t.Errorf("last contact = %q, want 2026-11-15", got.LastContact)
	}
	if _, err := s.LogTouch(Interaction{PersonID: "nope", Date: "2026-11-15"}); err == nil {
		t.Error("touching an unknown person should fail")
	}
}

func TestBirthdays(t *testing.T) {
	s := testStore(t)
	add(t, s, Person{Name: "Today", Birthday: "11-16"})
	add(t, s, Person{Name: "InTen", Birthday: "11-26"})
	add(t, s, Person{Name: "YearWrap", Birthday: "01-05"}) // 50 days out
	add(t, s, Person{Name: "None"})

	got := s.Birthdays(now, 30)
	if len(got) != 2 || got[0].Person.Name != "Today" || got[0].InDays != 0 ||
		got[1].Person.Name != "InTen" || got[1].InDays != 10 {
		t.Fatalf("30 days: %+v", got)
	}
	wrap := s.Birthdays(now, 60)
	if len(wrap) != 3 || wrap[2].Person.Name != "YearWrap" || wrap[2].Date.Year() != 2027 {
		t.Fatalf("the year wrap is missing: %+v", wrap)
	}

	on := s.BirthdaysOn(context.Background(), time.Date(2026, 11, 16, 0, 0, 0, 0, nyc))
	if len(on) != 1 || on[0].Name != "Today" {
		t.Errorf("BirthdaysOn = %+v", on)
	}
}

func TestConnections(t *testing.T) {
	s := testStore(t)
	a := add(t, s, Person{Name: "Quincy"})
	b := add(t, s, Person{Name: "Will"})
	c := add(t, s, Person{Name: "Dad"})

	if _, err := s.Connect(a.ID, a.ID, "self", "you"); err == nil {
		t.Error("self-connections should be refused")
	}
	if _, err := s.Connect(a.ID, "ghost", "x", "you"); err == nil {
		t.Error("connecting to a missing person should be refused")
	}
	// The same pair in either order is one connection.
	first, err := s.Connect(b.ID, a.ID, "trades", "you")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Connect(a.ID, b.ID, "future subcontractors", "you")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != again.ID || len(s.Connections()) != 1 {
		t.Fatalf("the pair should stay one connection: %+v vs %+v", first, again)
	}
	if again.Label != "future subcontractors" {
		t.Errorf("re-linking should update the label, got %q", again.Label)
	}
	if _, err := s.Connect(a.ID, c.ID, "father", "you"); err != nil {
		t.Fatal(err)
	}

	of := s.ConnectionsOf(a.ID)
	if len(of) != 2 || of[0].Other.Name != "Dad" || of[1].Other.Name != "Will" {
		t.Fatalf("ConnectionsOf: %+v", of)
	}

	// Deleting a person removes their edges.
	if err := s.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.Connections()) != 1 {
		t.Errorf("Will's edge should be gone: %+v", s.Connections())
	}
	if err := s.Disconnect(s.Connections()[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(s.Connections()) != 0 {
		t.Error("disconnect left an edge behind")
	}
}
