package main

import (
	"strings"
	"testing"
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
	if registry.label != "bufbuild/registry@commit111111" {
		t.Errorf("registry label = %q, want %q", registry.label, "bufbuild/registry@commit111111")
	}
	if registry.href != "https://buf.build/bufbuild/registry/commits/commit1111111111111111111111111" {
		t.Errorf("registry href = %q", registry.href)
	}

	protovalidate := nodes["commit2222222222222222222222222"]
	if protovalidate.label != "alice/protovalidate@commit222222" {
		t.Errorf("protovalidate label = %q, want %q (Organization vs User owner resolution)", protovalidate.label, "alice/protovalidate@commit222222")
	}
}

func TestCommitDepNodes_UnresolvedModuleFallsBackToCommitID(t *testing.T) {
	t.Parallel()

	commits := []*modulev1.Commit{{Id: "orphan-commit", ModuleId: "missing-module"}}
	nodes := commitDepNodes("buf.build", commits, nil, nil)

	node := nodes["orphan-commit"]
	if node.label != "orphan-commit" {
		t.Errorf("label = %q, want fallback to commit ID", node.label)
	}
	if node.href != "" {
		t.Errorf("href = %q, want empty when module can't be resolved", node.href)
	}
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
	if got := strings.Count(rendered, "bufbuild/protovalidate@ghi789"); got != 2 {
		t.Errorf("protovalidate appeared %d times in the tree, want 2 (once per parent)\n%s", got, rendered)
	}
	if !strings.Contains(rendered, "\x1b]8;;https://buf.build/bufbuild/registry/commits/abc123\x07") {
		t.Errorf("expected root node to carry an OSC 8 hyperlink to its commit page:\n%s", rendered)
	}
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
		if !strings.Contains(rendered, "b") {
			t.Errorf("expected cyclic dependency %q to still appear:\n%s", "b", rendered)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("renderDepsTree did not return -- likely infinite recursion on a cycle")
	}
}
