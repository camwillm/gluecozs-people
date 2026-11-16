package people

import (
	"testing"
)

func TestBuildGraph(t *testing.T) {
	s := testStore(t)
	kwame := add(t, s, Person{Name: "Kwame", Company: "DataCo", Tags: []string{"tech", "vibes"}})
	steve := add(t, s, Person{Name: "Steve", Company: "UMMS", Tags: []string{"healthcare"}})
	nas := add(t, s, Person{Name: "Nas", Company: "UMMS", Tags: []string{"healthcare"}})
	mom := add(t, s, Person{Name: "Mom", Company: "eXp", Tags: []string{"family", "real estate"},
		CadenceDays: 7, LastContact: "2026-10-01"}) // overdue
	ghost := add(t, s, Person{Name: "Old", Tags: []string{"tech"}})
	ghost.Archived = true
	if err := s.Update(ghost); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Connect(steve.ID, nas.ID, "Nas introduced us", "you"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Connect(kwame.ID, mom.ID, "met at the cookout", "ai"); err != nil {
		t.Fatal(err)
	}

	g := BuildGraph(s, s.Due(now), "")

	if len(g.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4 (archived people stay off the map): %+v", len(g.Nodes), g.Nodes)
	}
	var momNode *GraphNode
	for i := range g.Nodes {
		if g.Nodes[i].Name == "Mom" {
			momNode = &g.Nodes[i]
		}
	}
	if momNode == nil || !momNode.Due || momNode.Tag != "family" {
		t.Errorf("Mom's node should be marked due with her first tag: %+v", momNode)
	}

	var explicit, company int
	for _, l := range g.Links {
		switch l.Kind {
		case "link":
			explicit++
		case "company":
			company++
			if l.S != steve.ID && l.T != steve.ID {
				t.Errorf("the only shared company is UMMS: %+v", l)
			}
		}
	}
	// Steve–Nas share a company AND have an explicit link: only the explicit
	// line is drawn, so no dashed company line should exist for them.
	if explicit != 2 || company != 0 {
		t.Fatalf("links = %d explicit, %d company; want 2 and 0: %+v", explicit, company, g.Links)
	}

	// Remove the explicit Steve–Nas link: the company line appears instead.
	for _, c := range s.Connections() {
		if c.Label == "Nas introduced us" {
			if err := s.Disconnect(c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	g = BuildGraph(s, nil, "")
	company = 0
	for _, l := range g.Links {
		if l.Kind == "company" {
			company++
		}
	}
	if company != 1 {
		t.Fatalf("the same-company pair should get a dashed line: %+v", g.Links)
	}

	// Tag filter keeps only that bucket, and drops links that leave it.
	g = BuildGraph(s, nil, "healthcare")
	if len(g.Nodes) != 2 || len(g.Links) != 1 || g.Links[0].Kind != "company" {
		t.Fatalf("healthcare filter: %d nodes, %+v links", len(g.Nodes), g.Links)
	}
	g = BuildGraph(s, nil, "tech")
	if len(g.Nodes) != 1 || len(g.Links) != 0 {
		t.Fatalf("tech filter should drop the Kwame–Mom link: %d nodes, %+v", len(g.Nodes), g.Links)
	}
}
