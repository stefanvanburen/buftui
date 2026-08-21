package main

import (
	"strings"
	"testing"

	"go.vanburen.xyz/ok"
	"time"

	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	"charm.land/bubbles/v2/tree"
)

func TestCommitDepNodes(t *testing.T) {
	t.Parallel()

	commits := []*modulev1.Commit{
		{Id: "commit1111111111111111111111111", ModuleId: "mod-registry"},
		{Id: "commit2222222222222222222222222", ModuleId: "mod-protovalidate"},
	}
	modules := []*modulev1.Module{
		{Id: "mod-registry", Name: "registry", OwnerId: "org-bufbuild"},
		{Id: "mod-protovalidate", Name: "protovalidate", OwnerId: "user-alice"},
	}
	owners := []*ownerv1.Owner{
		{Value: &ownerv1.Owner_Organization{Organization: &ownerv1.Organization{Id: "org-bufbuild", Name: "bufbuild"}}},
		{Value: &ownerv1.Owner_User{User: &ownerv1.User{Id: "user-alice", Name: "alice"}}},
	}

	nodes := commitDepNodes("buf.build", commits, modules, owners)

	registry := nodes["commit1111111111111111111111111"]
	ok.Equal(t, registry.label, "bufbuild/registry@commit111111", ok.Sprintf("registry label"))
	ok.Equal(t, registry.href, "https://buf.build/bufbuild/registry/commits/commit1111111111111111111111111",
		ok.Sprintf("registry href"))

	protovalidate := nodes["commit2222222222222222222222222"]
	ok.Equal(t, protovalidate.label, "alice/protovalidate@commit222222",
		ok.Sprintf("protovalidate label (Organization vs User owner resolution)"))
}

func TestCommitDepNodes_UnresolvedModuleFallsBackToCommitID(t *testing.T) {
	t.Parallel()

	commits := []*modulev1.Commit{{Id: "orphan-commit", ModuleId: "missing-module"}}
	nodes := commitDepNodes("buf.build", commits, nil, nil)

	node := nodes["orphan-commit"]
	ok.Equal(t, node.label, "orphan-commit", ok.Sprintf("label should fall back to the commit ID"))
	ok.Zero(t, node.href, ok.Sprintf("href should be empty when the module can't be resolved"))
}

func TestDepsTree_DiamondDependencyAndHyperlinks(t *testing.T) {
	t.Parallel()

	// registry -> bufplugin -> protovalidate
	// registry -> protovalidate directly too (the diamond).
	edges := []*modulev1.Graph_Edge{
		{FromNode: &modulev1.Graph_Node{CommitId: "registry"}, ToNode: &modulev1.Graph_Node{CommitId: "bufplugin"}},
		{FromNode: &modulev1.Graph_Node{CommitId: "registry"}, ToNode: &modulev1.Graph_Node{CommitId: "protovalidate"}},
		{FromNode: &modulev1.Graph_Node{CommitId: "bufplugin"}, ToNode: &modulev1.Graph_Node{CommitId: "protovalidate"}},
	}
	nodes := map[string]depNode{
		"registry":      {label: "bufbuild/registry@abc123", href: "https://buf.build/bufbuild/registry/commits/abc123"},
		"bufplugin":     {label: "bufbuild/bufplugin@def456", href: "https://buf.build/bufbuild/bufplugin/commits/def456"},
		"protovalidate": {label: "bufbuild/protovalidate@ghi789", href: "https://buf.build/bufbuild/protovalidate/commits/ghi789"},
	}

	rendered := depsTree("registry", edges, nodes).String()

	// protovalidate is a dependency of both registry and bufplugin -- it
	// should appear twice (once under each parent), not deduplicated away.
	ok.Equal(t, strings.Count(rendered, "bufbuild/protovalidate@ghi789"), 2,
		ok.Sprintf("protovalidate should appear once per parent in the tree\n%s", rendered))
	ok.True(t, strings.Contains(rendered, "\x1b]8;;https://buf.build/bufbuild/registry/commits/abc123\x07"),
		ok.Sprintf("expected the root node to carry an OSC 8 hyperlink to its commit page:\n%s", rendered))
}

func TestDepsTree_CycleDoesNotInfinitelyRecurse(t *testing.T) {
	t.Parallel()

	// The real API guarantees a DAG, but if that ever didn't hold, a cycle
	// must render as a leaf rather than recursing forever.
	edges := []*modulev1.Graph_Edge{
		{FromNode: &modulev1.Graph_Node{CommitId: "a"}, ToNode: &modulev1.Graph_Node{CommitId: "b"}},
		{FromNode: &modulev1.Graph_Node{CommitId: "b"}, ToNode: &modulev1.Graph_Node{CommitId: "a"}},
	}
	nodes := map[string]depNode{
		"a": {label: "a"},
		"b": {label: "b"},
	}

	done := make(chan string, 1)
	go func() { done <- depsTree("a", edges, nodes).String() }()
	select {
	case rendered := <-done:
		ok.True(t, strings.Contains(rendered, "b"),
			ok.Sprintf("expected cyclic dependency %q to still appear:\n%s", "b", rendered))
	case <-time.After(2 * time.Second):
		ok.True(t, false, ok.Sprintf("depsTree did not return -- likely infinite recursion on a cycle"))
	}
}

func TestDepsTreeModel_NavigationCollapseAndHyperlinks(t *testing.T) {
	t.Parallel()

	edges := []*modulev1.Graph_Edge{
		{FromNode: &modulev1.Graph_Node{CommitId: "registry"}, ToNode: &modulev1.Graph_Node{CommitId: "bufplugin"}},
		{FromNode: &modulev1.Graph_Node{CommitId: "bufplugin"}, ToNode: &modulev1.Graph_Node{CommitId: "protovalidate"}},
	}
	nodes := map[string]depNode{
		"registry":      {label: "bufbuild/registry@abc123", href: "https://buf.build/bufbuild/registry/commits/abc123"},
		"bufplugin":     {label: "bufbuild/bufplugin@def456", href: "https://buf.build/bufbuild/bufplugin/commits/def456"},
		"protovalidate": {label: "bufbuild/protovalidate@ghi789", href: "https://buf.build/bufbuild/protovalidate/commits/ghi789"},
	}

	// Set up in the same order the app does: styles first, on an empty
	// tree, then the nodes once the graph arrives.
	model := tree.New(nil, 80, 20)
	model.SetShowHelp(false)
	model.SetStyles(depsTreeStyles(true))
	model.SetNodes(depsTree("registry", edges, nodes))

	// The cursor starts on the root.
	root, found := selectedDepNode(model)
	ok.True(t, found, ok.Sprintf("expected a selected node"))
	ok.Equal(t, root.label, "bufbuild/registry@abc123", ok.Sprintf("cursor should start on the root"))

	// Styling the tree must not eat the OSC 8 hyperlinks -- they're how the
	// labels stay clickable.
	view := model.View()
	ok.True(t, strings.Contains(view, "\x1b]8;;https://buf.build/bufbuild/bufplugin/commits/def456"),
		ok.Sprintf("expected the styled view to keep node hyperlinks:\n%q", view))

	// Moving down selects the first dependency, whose href drives yank/browse.
	model.Down()
	dep, found := selectedDepNode(model)
	ok.True(t, found, ok.Sprintf("expected a selected node after moving down"))
	ok.Equal(t, dep.label, "bufbuild/bufplugin@def456", ok.Sprintf("cursor should move to the first dependency"))
	ok.Equal(t, dep.href, "https://buf.build/bufbuild/bufplugin/commits/def456", ok.Sprintf("selected node href"))

	// Collapsing a node hides its transitive dependencies.
	model.CloseCurrentNode()
	ok.True(t, !strings.Contains(model.View(), "bufbuild/protovalidate@ghi789"),
		ok.Sprintf("closing bufplugin should hide protovalidate:\n%s", model.View()))
	model.OpenCurrentNode()
	ok.True(t, strings.Contains(model.View(), "bufbuild/protovalidate@ghi789"),
		ok.Sprintf("reopening bufplugin should show protovalidate again:\n%s", model.View()))
}

func TestReachableDepCount(t *testing.T) {
	t.Parallel()

	edge := func(from, to string) *modulev1.Graph_Edge {
		return &modulev1.Graph_Edge{
			FromNode: &modulev1.Graph_Node{CommitId: from},
			ToNode:   &modulev1.Graph_Node{CommitId: to},
		}
	}

	// A shared dep is rendered under each of its parents, but counts once.
	diamond := []*modulev1.Graph_Edge{
		edge("registry", "bufplugin"),
		edge("registry", "protovalidate"),
		edge("bufplugin", "protovalidate"),
	}
	ok.Equal(t, reachableDepCount("registry", diamond), 2,
		ok.Sprintf("protovalidate is reachable twice but is one dependency"))

	ok.Equal(t, reachableDepCount("registry", nil), 0,
		ok.Sprintf("a commit with no edges has no dependencies"))

	// A cycle must terminate, and must not count the root as its own dep.
	cycle := []*modulev1.Graph_Edge{edge("a", "b"), edge("b", "a")}
	done := make(chan int, 1)
	go func() { done <- reachableDepCount("a", cycle) }()
	select {
	case count := <-done:
		ok.Equal(t, count, 1, ok.Sprintf("only b is a dependency of a"))
	case <-time.After(2 * time.Second):
		ok.True(t, false, ok.Sprintf("reachableDepCount did not return -- likely looping on a cycle"))
	}
}
