package main

import (
	"strings"
	"testing"

	"go.vanburen.xyz/ok"
	"time"

	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
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

func TestRenderDepsTree_DiamondDependencyAndHyperlinks(t *testing.T) {
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

	rendered := renderDepsTree("registry", edges, nodes)

	// protovalidate is a dependency of both registry and bufplugin -- it
	// should appear twice (once under each parent), not deduplicated away.
	ok.Equal(t, strings.Count(rendered, "bufbuild/protovalidate@ghi789"), 2,
		ok.Sprintf("protovalidate should appear once per parent in the tree\n%s", rendered))
	ok.True(t, strings.Contains(rendered, "\x1b]8;;https://buf.build/bufbuild/registry/commits/abc123\x07"),
		ok.Sprintf("expected the root node to carry an OSC 8 hyperlink to its commit page:\n%s", rendered))
}

func TestRenderDepsTree_CycleDoesNotInfinitelyRecurse(t *testing.T) {
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
	go func() { done <- renderDepsTree("a", edges, nodes) }()
	select {
	case rendered := <-done:
		ok.True(t, strings.Contains(rendered, "b"),
			ok.Sprintf("expected cyclic dependency %q to still appear:\n%s", "b", rendered))
	case <-time.After(2 * time.Second):
		ok.True(t, false, ok.Sprintf("renderDepsTree did not return -- likely infinite recursion on a cycle"))
	}
}
