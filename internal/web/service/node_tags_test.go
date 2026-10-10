package service

import (
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestNormalizeNodeTags(t *testing.T) {
	long := strings.Repeat("a", maxNodeTagLen+1)
	many := make([]string, maxNodeTags+1)
	for i := range many {
		many[i] = "t" + strings.Repeat("x", i)
	}
	for name, tc := range map[string]struct {
		in   []string
		want []string
		bad  string
	}{
		"nothing":            {in: nil, want: nil},
		"only blanks":        {in: []string{"", "  "}, want: nil},
		"trims and keeps":    {in: []string{" eu ", "exit"}, want: []string{"eu", "exit"}},
		"repeats by case":    {in: []string{"EU", "eu", "Eu", "exit"}, want: []string{"EU", "exit"}},
		"a long tag":         {in: []string{long}, bad: "longer than"},
		"a control char":     {in: []string{"a\nb"}, bad: "control character"},
		"too many":           {in: many, bad: "at most"},
		"as many as allowed": {in: many[:maxNodeTags], want: many[:maxNodeTags]},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeNodeTags(tc.in)
			if tc.bad != "" {
				if err == nil || !strings.Contains(err.Error(), tc.bad) {
					t.Fatalf("normalizeNodeTags = %v, %v, want an error that says %q", got, err, tc.bad)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("normalizeNodeTags(%q) = %q, %v, want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestAgentNodeTagsAreSavedAndReplaced(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, Tags: []string{" EU ", "eu", "exit"}})
	if err != nil {
		t.Fatal(err)
	}
	id := pairing.Node.Id
	if want := []string{"EU", "exit"}; !slices.Equal(pairing.Node.Tags, want) {
		t.Fatalf("the new agent's tags = %q, want %q", pairing.Node.Tags, want)
	}

	save := func(tags []string) []string {
		t.Helper()
		req := &NodeMutationRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, Enable: true, Tags: tags}
		if err := svc.UpdateFromRequest(id, req); err != nil {
			t.Fatalf("UpdateFromRequest: %v", err)
		}
		view, err := svc.GetViewById(id)
		if err != nil {
			t.Fatal(err)
		}
		return view.Tags
	}
	if got, want := save([]string{"us", "US"}), []string{"us"}; !slices.Equal(got, want) {
		t.Fatalf("a save that names tags left %q, want %q", got, want)
	}
	if got := save([]string{"eu"}); !slices.Equal(got, []string{"eu"}) {
		t.Fatalf("a save that names other tags left %q, want them to replace the stored ones", got)
	}
	// A form-encoded request cannot say an empty list, so no tags is the way to clear them.
	if got := save(nil); got == nil || len(got) != 0 {
		t.Fatalf("a save that names no tags left %#v, want none, as an empty list", got)
	}

	stored, err := svc.AgentTags(id)
	if err != nil || len(stored) != 0 {
		t.Fatalf("AgentTags = %q, %v, want none after the clear", stored, err)
	}
	if err := svc.UpdateFromRequest(id, &NodeMutationRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, Enable: true, Tags: []string{"bad\ttag"}}); err == nil {
		t.Fatal("a tag with a control character was accepted")
	}
}

func TestOnlyAnAgentNodeCarriesTags(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	token := "token"
	view, err := svc.CreateFromRequest(&NodeMutationRequest{
		Name: "stock", Address: "203.0.113.9", Port: 2053, Enable: true, ApiToken: &token, Tags: []string{"eu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Tags == nil || len(view.Tags) != 0 {
		t.Fatalf("a stock panel node came back with tags %#v", view.Tags)
	}
	if err := svc.UpdateFromRequest(view.Id, &NodeMutationRequest{
		Name: "stock", Address: "203.0.113.9", Port: 2053, Enable: true, Tags: []string{"eu"},
	}); err != nil {
		t.Fatal(err)
	}
	if tags, err := svc.AgentTags(view.Id); err != nil || len(tags) != 0 {
		t.Fatalf("AgentTags of a stock panel node = %q, %v, want none: it runs the routing of its own panel", tags, err)
	}

	node := &model.Node{Name: "model", Address: "203.0.113.10", Port: 2053, Scheme: "https", Tags: []string{"eu"}}
	if err := svc.Create(node); err != nil {
		t.Fatal(err)
	}
	if len(node.Tags) != 0 {
		t.Fatalf("Create left tags %q on a stock panel node", node.Tags)
	}
}
