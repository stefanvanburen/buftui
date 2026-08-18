package main

import (
	"context"
	"fmt"
	"slices"

	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2/tree"
	"connectrpc.com/connect"
)

// depsMsg carries the rendered dependency tree for a commit.
type depsMsg struct {
	rendered string
}

type depsErrMsg struct{ err error }

// getDeps fetches the full transitive dependency graph for commitID, resolves
// every node's owner/module name, and renders it as a tree rooted at
// commitID, with each node hyperlinked (OSC 8) to its commit page on remote.
// Edges are from_node -> to_node meaning "from_node depends on to_node"
// (verified against the real BSR: e.g. bufbuild/registry -> bufbuild/bufplugin
// -> bufbuild/protovalidate).
func (c *client) getDeps(commitID, remote string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()

		graphResp, err := c.graphServiceClient.GetGraph(ctx, connect.NewRequest(&modulev1.GetGraphRequest{
			ResourceRefs: []*modulev1.ResourceRef{{
				Value: &modulev1.ResourceRef_Id{Id: commitID},
			}},
		}))
		if err != nil {
			return depsErrMsg{fmt.Errorf("getting dependency graph: %w", err)}
		}
		graph := graphResp.Msg.Graph

		// Resolve every commit's module_id to a Module (id, name, owner_id) in
		// one batched call -- not once per commit.
		moduleIDs := uniqueModuleIDs(graph.Commits)
		moduleRefs := make([]*modulev1.ModuleRef, len(moduleIDs))
		for i, id := range moduleIDs {
			moduleRefs[i] = &modulev1.ModuleRef{Value: &modulev1.ModuleRef_Id{Id: id}}
		}
		modulesResp, err := c.moduleServiceClient.GetModules(ctx, connect.NewRequest(&modulev1.GetModulesRequest{
			ModuleRefs: moduleRefs,
		}))
		if err != nil {
			return depsErrMsg{fmt.Errorf("resolving dependency modules: %w", err)}
		}

		// Resolve every module's owner_id to an Owner (User or Organization)
		// name, again in one batched call.
		ownerIDs := uniqueOwnerIDs(modulesResp.Msg.Modules)
		ownerRefs := make([]*ownerv1.OwnerRef, len(ownerIDs))
		for i, id := range ownerIDs {
			ownerRefs[i] = &ownerv1.OwnerRef{Value: &ownerv1.OwnerRef_Id{Id: id}}
		}
		ownersResp, err := c.ownerServiceClient.GetOwners(ctx, connect.NewRequest(&ownerv1.GetOwnersRequest{
			OwnerRefs: ownerRefs,
		}))
		if err != nil {
			return depsErrMsg{fmt.Errorf("resolving dependency owners: %w", err)}
		}

		nodes := commitDepNodes(remote, graph.Commits, modulesResp.Msg.Modules, ownersResp.Msg.Owners)
		rendered := renderDepsTree(commitID, graph.Edges, nodes)
		return depsMsg{rendered: rendered}
	}
}

// uniqueModuleIDs returns the distinct module_ids referenced by commits, in
// first-seen order (for deterministic request ordering, easier to debug than
// a random map iteration order).
func uniqueModuleIDs(commits []*modulev1.Commit) []string {
	var ids []string
	seen := make(map[string]bool)
	for _, c := range commits {
		if !seen[c.ModuleId] {
			seen[c.ModuleId] = true
			ids = append(ids, c.ModuleId)
		}
	}
	return ids
}

// uniqueOwnerIDs returns the distinct owner_ids referenced by modules, in
// first-seen order.
func uniqueOwnerIDs(modules []*modulev1.Module) []string {
	var ids []string
	seen := make(map[string]bool)
	for _, m := range modules {
		if !seen[m.OwnerId] {
			seen[m.OwnerId] = true
			ids = append(ids, m.OwnerId)
		}
	}
	return ids
}

// ownerName returns the display name of a User or Organization Owner.
func ownerName(o *ownerv1.Owner) string {
	switch v := o.Value.(type) {
	case *ownerv1.Owner_User:
		return v.User.Name
	case *ownerv1.Owner_Organization:
		return v.Organization.Name
	default:
		return ""
	}
}

// depNode is a single node's display text (plain, for sorting) and its
// hyperlink target -- the commit's page on the BSR.
type depNode struct {
	label string
	href  string
}

// commitDepNodes maps each commit ID in the graph to a depNode of the form
// "owner/module@shortdigest", linked to https://remote/owner/module/commits/id.
func commitDepNodes(remote string, commits []*modulev1.Commit, modules []*modulev1.Module, owners []*ownerv1.Owner) map[string]depNode {
	moduleByID := make(map[string]*modulev1.Module, len(modules))
	for _, m := range modules {
		moduleByID[m.Id] = m
	}
	ownerNameByID := make(map[string]string, len(owners))
	for _, o := range owners {
		ownerNameByID[ownerIDOf(o)] = ownerName(o)
	}

	nodes := make(map[string]depNode, len(commits))
	for _, c := range commits {
		module := moduleByID[c.ModuleId]
		if module == nil {
			nodes[c.Id] = depNode{label: c.Id}
			continue
		}
		owner := ownerNameByID[module.OwnerId]
		ref := c.Id
		if len(ref) > 12 {
			ref = ref[:12]
		}
		nodes[c.Id] = depNode{
			label: fmt.Sprintf("%s/%s@%s", owner, module.Name, ref),
			href:  fmt.Sprintf("https://%s/%s/%s/commits/%s", remote, owner, module.Name, c.Id),
		}
	}
	return nodes
}

// ownerIDOf returns the id of a User or Organization Owner.
func ownerIDOf(o *ownerv1.Owner) string {
	switch v := o.Value.(type) {
	case *ownerv1.Owner_User:
		return v.User.Id
	case *ownerv1.Owner_Organization:
		return v.Organization.Id
	default:
		return ""
	}
}

// renderDepsTree renders the dependency graph rooted at rootCommitID as a
// tree, using edges (from_node depends on to_node) to find each node's
// direct dependencies. Shared dependencies are rendered once under every
// parent that requires them, matching how graph-visualization tools
// typically display a DAG as a tree; a per-render visited-on-this-path set
// guards against runaway recursion should the graph ever not be a DAG. Each
// node is rendered as an OSC 8 hyperlink to its commit page.
func renderDepsTree(rootCommitID string, edges []*modulev1.Graph_Edge, nodes map[string]depNode) string {
	children := make(map[string][]string)
	for _, e := range edges {
		children[e.FromNode.CommitId] = append(children[e.FromNode.CommitId], e.ToNode.CommitId)
	}
	for _, deps := range children {
		slices.SortFunc(deps, func(a, b string) int {
			return compareLabels(nodes[a].label, nodes[b].label)
		})
	}

	root := tree.Root(renderDepNode(rootCommitID, nodes))
	buildDepsTree(root, rootCommitID, children, nodes, map[string]bool{rootCommitID: true})
	return root.String()
}

func buildDepsTree(node *tree.Tree, commitID string, children map[string][]string, nodes map[string]depNode, visited map[string]bool) {
	for _, depID := range children[commitID] {
		if visited[depID] {
			// Already an ancestor of this path; render as a leaf to avoid
			// infinite recursion instead of silently dropping it.
			node.Child(renderDepNode(depID, nodes))
			continue
		}
		child := tree.Root(renderDepNode(depID, nodes))
		visited[depID] = true
		buildDepsTree(child, depID, children, nodes, visited)
		delete(visited, depID)
		node.Child(child)
	}
}

// renderDepNode renders a node's label, hyperlinked to its commit page when
// one is known.
func renderDepNode(commitID string, nodes map[string]depNode) string {
	node, ok := nodes[commitID]
	if !ok {
		return commitID
	}
	if node.href == "" {
		return node.label
	}
	return renderHyperlink(node.label, node.href)
}

func compareLabels(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
