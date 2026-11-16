package people

import (
	"sort"
	"strings"
)

// The galaxy page draws people as stars and their links as lines. The data
// below is built server side and embedded in the page as JSON; a small
// hand-written force layout in the template does the drawing.

// GraphNode is one person on the map.
type GraphNode struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Tag     string `json:"tag,omitempty"` // first tag: sets the color
	Company string `json:"company,omitempty"`
	Due     bool   `json:"due,omitempty"`
}

// GraphLink is one line. Kind "link" is a connection you (or the AI, after
// your confirm) made, with its label. Kind "company" is an implied line
// between people at the same company, drawn dashed.
type GraphLink struct {
	S     string `json:"s"`
	T     string `json:"t"`
	Label string `json:"label,omitempty"`
	Kind  string `json:"kind"`
}

// Graph is everything the galaxy page needs.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Links []GraphLink `json:"links"`
	Tags  []string    `json:"tags"` // legend, in a stable order
}

// BuildGraph turns the book into the galaxy. Archived people stay off the
// map. tagFilter, when set, keeps only people carrying that tag (and the
// links between them).
func BuildGraph(s *Store, due []DuePerson, tagFilter string) Graph {
	dueSet := map[string]bool{}
	for _, d := range due {
		dueSet[d.Person.ID] = true
	}

	g := Graph{Nodes: []GraphNode{}, Links: []GraphLink{}, Tags: []string{}}
	keep := map[string]Person{}
	tagSeen := map[string]bool{}
	for _, p := range s.All() {
		if p.Archived {
			continue
		}
		if tagFilter != "" && !hasTagFold(p.Tags, tagFilter) {
			continue
		}
		keep[p.ID] = p
		tag := ""
		if len(p.Tags) > 0 {
			tag = p.Tags[0]
		}
		if tag != "" && !tagSeen[strings.ToLower(tag)] {
			tagSeen[strings.ToLower(tag)] = true
			g.Tags = append(g.Tags, tag)
		}
		g.Nodes = append(g.Nodes, GraphNode{
			ID: p.ID, Name: p.Name, Tag: tag, Company: p.Company, Due: dueSet[p.ID],
		})
	}
	sort.Strings(g.Tags)

	linked := map[string]bool{}
	for _, c := range s.Connections() {
		if _, ok := keep[c.A]; !ok {
			continue
		}
		if _, ok := keep[c.B]; !ok {
			continue
		}
		linked[c.A+"|"+c.B] = true
		g.Links = append(g.Links, GraphLink{S: c.A, T: c.B, Label: c.Label, Kind: "link"})
	}

	// Same company: an implied, dashed line, unless an explicit link
	// already joins the pair.
	byCompany := map[string][]string{}
	for id, p := range keep {
		co := strings.ToLower(strings.TrimSpace(p.Company))
		if co == "" {
			continue
		}
		byCompany[co] = append(byCompany[co], id)
	}
	for _, ids := range byCompany {
		sort.Strings(ids)
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				if linked[ids[i]+"|"+ids[j]] {
					continue
				}
				g.Links = append(g.Links, GraphLink{S: ids[i], T: ids[j], Kind: "company"})
			}
		}
	}
	sort.SliceStable(g.Links, func(i, j int) bool {
		if g.Links[i].Kind != g.Links[j].Kind {
			return g.Links[i].Kind < g.Links[j].Kind
		}
		if g.Links[i].S != g.Links[j].S {
			return g.Links[i].S < g.Links[j].S
		}
		return g.Links[i].T < g.Links[j].T
	})
	return g
}

func hasTagFold(tags []string, want string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, want) {
			return true
		}
	}
	return false
}
