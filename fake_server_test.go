package main

import (
	"connectrpc.com/connect"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"buf.build/gen/go/bufbuild/registry/connectrpc/go/buf/registry/module/v1/modulev1connect"
	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"go.akshayshah.org/attest"
	"go.akshayshah.org/memhttp"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeModuleServiceHandler implements the ModuleService for testing. delay,
// if set, is slept before responding -- used to simulate a slow/hanging
// server for RPC timeout tests.
type fakeModuleServiceHandler struct {
	modulev1connect.UnimplementedModuleServiceHandler
	delay time.Duration
}

func (f *fakeModuleServiceHandler) ListModules(
	ctx context.Context,
	req *connect.Request[modulev1.ListModulesRequest],
) (*connect.Response[modulev1.ListModulesResponse], error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	modules := []*modulev1.Module{
		{
			Id:          "mod1",
			Name:        "bufbuild/registry",
			OwnerId:     "owner1",
			Description: "The Buf registry module",
			Visibility:  modulev1.ModuleVisibility_MODULE_VISIBILITY_PUBLIC,
			State:       modulev1.ModuleState_MODULE_STATE_ACTIVE,
			CreateTime:  timestamppb.New(time.Now().Add(-24 * time.Hour)),
			UpdateTime:  timestamppb.New(time.Now()),
		},
		{
			Id:          "mod2",
			Name:        "bufbuild/protovalidate",
			OwnerId:     "owner1",
			Description: "Protocol buffer validation",
			Visibility:  modulev1.ModuleVisibility_MODULE_VISIBILITY_PUBLIC,
			State:       modulev1.ModuleState_MODULE_STATE_ACTIVE,
			CreateTime:  timestamppb.New(time.Now().Add(-48 * time.Hour)),
			UpdateTime:  timestamppb.New(time.Now()),
		},
	}

	response := connect.NewResponse(&modulev1.ListModulesResponse{
		Modules: modules,
	})
	return response, nil
}

// fakeCommitServiceHandler implements the CommitService for testing.
type fakeCommitServiceHandler struct {
	modulev1connect.UnimplementedCommitServiceHandler
}

func (f *fakeCommitServiceHandler) ListCommits(
	ctx context.Context,
	req *connect.Request[modulev1.ListCommitsRequest],
) (*connect.Response[modulev1.ListCommitsResponse], error) {
	commits := []*modulev1.Commit{
		{
			Id:         "abc123def456",
			CreateTime: timestamppb.New(time.Now().Add(-1 * time.Hour)),
		},
		{
			Id:         "def456ghi789",
			CreateTime: timestamppb.New(time.Now().Add(-2 * time.Hour)),
		},
		{
			Id:         "ghi789jkl012",
			CreateTime: timestamppb.New(time.Now().Add(-3 * time.Hour)),
		},
	}

	response := connect.NewResponse(&modulev1.ListCommitsResponse{
		Commits: commits,
	})
	return response, nil
}

// fakeDownloadServiceHandler implements the DownloadService for testing.
type fakeDownloadServiceHandler struct {
	modulev1connect.UnimplementedDownloadServiceHandler
}

func (f *fakeDownloadServiceHandler) Download(
	ctx context.Context,
	req *connect.Request[modulev1.DownloadRequest],
) (*connect.Response[modulev1.DownloadResponse], error) {
	contents := []*modulev1.DownloadResponse_Content{
		{
			Commit: &modulev1.Commit{
				Id:         "abc123def456",
				CreateTime: timestamppb.New(time.Now().Add(-1 * time.Hour)),
			},
			Files: []*modulev1.File{
				{
					Path:    "buf.yaml",
					Content: []byte("version: v1\nbreaking:\n  use: FILE\n"),
				},
				{
					Path:    "buf.gen.yaml",
					Content: []byte("version: v1\nplugins:\n  - remote: go\n"),
				},
				{
					Path:    "README.md",
					Content: []byte("# Registry\n\nThe Buf registry module.\n"),
				},
			},
		},
	}

	response := connect.NewResponse(&modulev1.DownloadResponse{
		Contents: contents,
	})
	return response, nil
}

// fakeResourceServiceHandler implements the ResourceService for testing.
type fakeResourceServiceHandler struct {
	modulev1connect.UnimplementedResourceServiceHandler
}

func (f *fakeResourceServiceHandler) GetResources(
	ctx context.Context,
	req *connect.Request[modulev1.GetResourcesRequest],
) (*connect.Response[modulev1.GetResourcesResponse], error) {
	resources := []*modulev1.Resource{}

	// Check what type of resource was requested
	for _, ref := range req.Msg.ResourceRefs {
		if nameRef, ok := ref.Value.(*modulev1.ResourceRef_Name_); ok {
			name := nameRef.Name
			// Return a module resource by default
			resource := &modulev1.Resource{
				Value: &modulev1.Resource_Module{
					Module: &modulev1.Module{
						Id:          "mod1",
						Name:        name.Module,
						OwnerId:     "owner1",
						Description: fmt.Sprintf("Test module %s", name.Module),
						Visibility:  modulev1.ModuleVisibility_MODULE_VISIBILITY_PUBLIC,
						State:       modulev1.ModuleState_MODULE_STATE_ACTIVE,
						CreateTime:  timestamppb.New(time.Now().Add(-24 * time.Hour)),
						UpdateTime:  timestamppb.New(time.Now()),
					},
				},
			}
			resources = append(resources, resource)
		}
	}

	response := connect.NewResponse(&modulev1.GetResourcesResponse{
		Resources: resources,
	})
	return response, nil
}

// fakeGraphServiceHandler implements the GraphService for testing. delay, if
// set, is slept before responding -- used to simulate a slow/hanging server.
// calls counts invocations, for tests asserting on caching behavior.
type fakeGraphServiceHandler struct {
	modulev1connect.UnimplementedGraphServiceHandler
	delay time.Duration
	calls atomic.Int32
}

func (f *fakeGraphServiceHandler) GetGraph(
	ctx context.Context,
	req *connect.Request[modulev1.GetGraphRequest],
) (*connect.Response[modulev1.GetGraphResponse], error) {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return connect.NewResponse(&modulev1.GetGraphResponse{
		Graph: &modulev1.Graph{},
	}), nil
}

// startFakeServer creates an in-memory Buf registry service and returns a client.
func startFakeServer(t *testing.T) *client {
	t.Helper()

	// Setup Connect handlers
	mux := http.NewServeMux()
	mux.Handle(modulev1connect.NewModuleServiceHandler(&fakeModuleServiceHandler{}))
	mux.Handle(modulev1connect.NewCommitServiceHandler(&fakeCommitServiceHandler{}))
	mux.Handle(modulev1connect.NewDownloadServiceHandler(&fakeDownloadServiceHandler{}))
	mux.Handle(modulev1connect.NewResourceServiceHandler(&fakeResourceServiceHandler{}))
	mux.Handle(modulev1connect.NewGraphServiceHandler(&fakeGraphServiceHandler{}))

	// Create in-memory HTTP server
	server, err := memhttp.New(mux)
	attest.Ok(t, err, attest.Fatal())

	// Cleanup
	t.Cleanup(func() {
		attest.Ok(t, server.Close())
	})

	// Return a client with all services
	return &client{
		moduleServiceClient:   modulev1connect.NewModuleServiceClient(server.Client(), "https://example.com"),
		commitServiceClient:   modulev1connect.NewCommitServiceClient(server.Client(), "https://example.com"),
		downloadServiceClient: modulev1connect.NewDownloadServiceClient(server.Client(), "https://example.com"),
		resourceServiceClient: modulev1connect.NewResourceServiceClient(server.Client(), "https://example.com"),
		graphServiceClient:    modulev1connect.NewGraphServiceClient(server.Client(), "https://example.com"),
	}
}

// startFakeServerWithSlowModuleList is like startFakeServer, but ListModules
// blocks for delay before responding -- used to simulate a slow/hanging BSR
// backend for RPC timeout tests.
func startFakeServerWithSlowModuleList(t *testing.T, delay time.Duration) *client {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(modulev1connect.NewModuleServiceHandler(&fakeModuleServiceHandler{delay: delay}))

	server, err := memhttp.New(mux)
	attest.Ok(t, err, attest.Fatal())
	t.Cleanup(func() {
		attest.Ok(t, server.Close())
	})

	return &client{
		moduleServiceClient: modulev1connect.NewModuleServiceClient(server.Client(), "https://example.com"),
	}
}

// startFakeServerForDocsCaching is like startFakeServer, but also returns
// the graph service handler so a test can inspect how many times it was
// actually called -- used to verify compileDocs serves a repeat request for
// the same commit from cache instead of hitting the network again.
func startFakeServerForDocsCaching(t *testing.T) (*client, *fakeGraphServiceHandler) {
	t.Helper()

	graphHandler := &fakeGraphServiceHandler{}

	mux := http.NewServeMux()
	mux.Handle(modulev1connect.NewModuleServiceHandler(&fakeModuleServiceHandler{}))
	mux.Handle(modulev1connect.NewCommitServiceHandler(&fakeCommitServiceHandler{}))
	mux.Handle(modulev1connect.NewDownloadServiceHandler(&fakeDownloadServiceHandler{}))
	mux.Handle(modulev1connect.NewResourceServiceHandler(&fakeResourceServiceHandler{}))
	mux.Handle(modulev1connect.NewGraphServiceHandler(graphHandler))

	server, err := memhttp.New(mux)
	attest.Ok(t, err, attest.Fatal())
	t.Cleanup(func() {
		attest.Ok(t, server.Close())
	})

	return &client{
		moduleServiceClient:   modulev1connect.NewModuleServiceClient(server.Client(), "https://example.com"),
		commitServiceClient:   modulev1connect.NewCommitServiceClient(server.Client(), "https://example.com"),
		downloadServiceClient: modulev1connect.NewDownloadServiceClient(server.Client(), "https://example.com"),
		resourceServiceClient: modulev1connect.NewResourceServiceClient(server.Client(), "https://example.com"),
		graphServiceClient:    modulev1connect.NewGraphServiceClient(server.Client(), "https://example.com"),
	}, graphHandler
}

// initialModel creates a model with a fake service client.
func newTestModel(c *client) model {
	delegate := list.NewDefaultDelegate()
	moduleList := list.New(nil, delegate, 20, 20)
	moduleList.SetShowHelp(false)

	commitList := list.New(nil, delegate, 20, 20)
	commitList.SetShowHelp(false)

	commitFilesList := list.New(nil, delegate, 20, 20)
	commitFilesList.SetShowHelp(false)

	docsList := list.New(nil, delegate, 20, 20)
	docsList.SetShowHelp(false)

	return model{
		state:            modelStateNavigating,
		spinner:          spinner.New(spinner.WithSpinner(spinner.Dot)),
		client:           c,
		help:             help.New(),
		keys:             keys,
		currentReference: nil,
		navigateInput:    newNavigateInput(),
		docsSearchInput:  newDocsSearchInput(),
		docsMatchIdx:     -1,
		remote:           "buf.build",
		fileViewport:     viewport.New(),
		docsViewport:     viewport.New(),

		moduleList:      moduleList,
		commitList:      commitList,
		commitFilesList: commitFilesList,
		docsList:        docsList,
	}
}

// TestInitialNavigatingState verifies the model starts in navigating state.
func TestInitialNavigatingState(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)

	attest.Equal(t, m.state, modelStateNavigating)
	attest.Equal(t, m.currentOwner, "")
	attest.Equal(t, m.currentModule, "")
	attest.Equal(t, m.err, nil)
}

// Note: In Bubble Tea v2, teatest is not yet available with the stable charm.land
// module path. Full UI/interaction tests can be added when teatest v2 is released
// for charm.land. For now, we test commands and rendering independently.
// See: https://charm.land/blog/v2/

// TestListModulesCommand tests the listModules client command.
func TestListModulesCommand(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := model{client: c}

	// Execute the listModules command
	cmd := m.client.listModules("bufbuild")
	attest.NotEqual(t, cmd, nil)

	// Run the command and check the result
	msg := cmd()

	// Should return a modulesMsg
	modules, ok := msg.(modulesMsg)
	attest.True(t, ok, attest.Sprintf("expected modulesMsg, got %T", msg))
	attest.True(t, len(modules) > 0, attest.Sprintf("expected modules, got %d", len(modules)))
	attest.Equal(t, modules[0].Name, "bufbuild/registry")
}

// TestListCommitsCommand tests the listCommits client command.
func TestListCommitsCommand(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := model{client: c}

	// Execute the listCommits command
	cmd := m.client.listCommits("bufbuild", "registry")
	attest.NotEqual(t, cmd, nil)

	// Run the command
	msg := cmd()

	// Should return a commitsMsg
	commits, ok := msg.(commitsMsg)
	attest.True(t, ok, attest.Sprintf("expected commitsMsg, got %T", msg))
	attest.True(t, len(commits.commits) > 0, attest.Sprintf("expected commits, got %d", len(commits.commits)))
	attest.Equal(t, commits.commits[0].Id, "abc123def456")
}

// TestGetCommitContentCommand tests the getCommitContent client command.
func TestGetCommitContentCommand(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := model{client: c}

	// Execute the getCommitContent command
	cmd := m.client.getCommitContent("abc123def456")
	attest.NotEqual(t, cmd, nil)

	// Run the command
	msg := cmd()

	// Should return a contentsMsg
	content, ok := msg.(contentsMsg)
	attest.True(t, ok, attest.Sprintf("expected contentsMsg, got %T", msg))
	attest.True(t, len(content.Files) > 0, attest.Sprintf("expected files, got %d", len(content.Files)))
	attest.Equal(t, content.Files[0].Path, "buf.yaml")
}

// TestViewDisplay tests that the model renders correctly in different states.
func TestViewDisplay(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*model)
		contains []string
	}{
		{
			name:     "navigating state shows help",
			setup:    func(m *model) { m.state = modelStateNavigating },
			contains: []string{"Navigate to owner", "enter", "esc"},
		},
		{
			name:     "navigating state",
			setup:    func(m *model) { m.state = modelStateNavigating },
			contains: []string{"Navigate to owner"},
		},
		{
			name: "loading modules",
			setup: func(m *model) {
				m.state = modelStateLoadingModules
			},
			contains: []string{"Loading modules"},
		},
		{
			name: "loading commits",
			setup: func(m *model) {
				m.state = modelStateLoadingCommits
			},
			contains: []string{"Loading commits"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := startFakeServer(t)
			m := newTestModel(c)
			tt.setup(&m)

			view := m.View()
			viewContent := view.Content
			for _, text := range tt.contains {
				attest.True(t, strings.Contains(viewContent, text), attest.Sprintf("view should contain %q", text))
			}
		})
	}
}

// TestModuleFilteringNoOSCCodes verifies that filtering the module list shows
// plain text titles with no OSC escape sequences, and that our keybindings
// (browse, yank, navigate) are suppressed while the list is filtering.
func TestModuleFilteringNoOSCCodes(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)

	// Populate the module list and currentModules (both are checked in View).
	mod1 := &modulev1.Module{Name: "registry"}
	mod2 := &modulev1.Module{Name: "protovalidate"}
	m.currentModules = modulesMsg{mod1, mod2}
	m.moduleList.SetItems([]list.Item{
		&module{underlying: mod1, remote: "buf.build", owner: "bufbuild"},
		&module{underlying: mod2, remote: "buf.build", owner: "bufbuild"},
	})
	m.state = modelStateBrowsingModules

	// Press "/" to enter filter mode.
	m2, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = m2.(model)
	attest.Equal(t, m.moduleList.FilterState(), list.Filtering)

	// Type a filter term one rune at a time.
	for _, r := range "reg" {
		m2, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = m2.(model)
	}

	// The rendered view must not contain OSC escape sequences (ESC ] 8).
	view := m.View()
	attest.False(t, strings.Contains(view.Content, "\x1b]8"), attest.Sprintf(
		"module list filter view should not contain OSC hyperlink escape codes"))

	// Module names should appear as plain text.
	attest.True(t, strings.Contains(view.Content, "registry"), attest.Sprintf(
		"module list filter view should contain plain module name"))

	// Keybindings that would normally trigger actions must not fire while
	// filtering — they should pass through to the list instead.
	for _, key := range []string{"o", "y", "g"} {
		m2, _ = m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		m = m2.(model)
		// The model must still be in filtering state — not navigating or erroring.
		attest.Equal(t, m.state, modelStateBrowsingModules)
		attest.Equal(t, m.moduleList.FilterState(), list.Filtering)
		attest.Equal(t, m.err, nil)
	}
}

// TestErrorRecovery verifies that API/network errors never halt the app.
// Each loading state should transition to a browsable state and surface the
// error as a status message or inline navigate error rather than setting m.err.
func TestErrorRecovery(t *testing.T) {
	t.Parallel()

	injected := fmt.Errorf("injected failure")

	tests := []struct {
		name            string
		initialState    modelState
		wantState       modelState
		wantNavigateErr bool
	}{
		{
			name:         "error while loading modules",
			initialState: modelStateLoadingModules,
			wantState:    modelStateBrowsingModules,
		},
		{
			name:         "error while loading commits",
			initialState: modelStateLoadingCommits,
			wantState:    modelStateBrowsingCommits,
		},
		{
			name:         "error while loading commit file contents",
			initialState: modelStateLoadingCommitFileContents,
			wantState:    modelStateBrowsingCommitContents,
		},
		{
			name:            "error while loading reference",
			initialState:    modelStateLoadingReference,
			wantState:       modelStateNavigating,
			wantNavigateErr: true,
		},
		{
			name:         "error while browsing commits (e.g. load more)",
			initialState: modelStateBrowsingCommits,
			wantState:    modelStateBrowsingCommits,
		},
		{
			name:            "error in unexpected state falls back to navigate",
			initialState:    modelStateLoadingModules - 1, // deliberate unknown state
			wantState:       modelStateNavigating,
			wantNavigateErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := startFakeServer(t)
			m := newTestModel(c)
			m.state = tt.initialState
			// A docs-compile error (e.g. a schema using a feature protodesc
			// can't build, like the legacy MessageSet wire format) must not
			// leave the docs tab spinning forever -- loadingDocs should be
			// cleared on any error, not just the docsMsg success path.
			m.loadingDocs = true

			m2, _ := m.Update(errMsg{err: injected})
			m = m2.(model)

			// Must never set m.err — that halts the app permanently.
			attest.Equal(t, m.err, nil)
			// Must land in the expected state.
			attest.Equal(t, m.state, tt.wantState)
			attest.False(t, m.loadingDocs, attest.Sprintf("loadingDocs should be cleared after any error, leaving the docs tab stuck spinning otherwise"))
			// An error that arrived while loadingDocs was true is a
			// docs-compile failure and must be recorded so the docs tab can
			// show it, rather than falling back to the misleading "No proto
			// files found" (as if the module were genuinely empty).
			attest.ErrorIs(t, m.docsErr, injected)
			// Navigate errors must be surfaced via navigateErr, not m.err.
			if tt.wantNavigateErr {
				attest.NotEqual(t, m.navigateErr, nil)
			} else {
				attest.Equal(t, m.navigateErr, nil)
			}
		})
	}
}

// TestListModules_TimesOut verifies that a slow/hanging BSR backend can't
// block the app forever: every RPC call must carry a bounded deadline
// rather than context.Background(), or a single stalled response leaves the
// UI stuck with no way to recover (as found live against
// buf.build/svanburen/protobuf-conformance, though that specific case turned
// out to be a fast error rather than a true hang).
func TestListModules_TimesOut(t *testing.T) {
	orig := rpcTimeout
	rpcTimeout = 50 * time.Millisecond
	t.Cleanup(func() { rpcTimeout = orig })

	c := startFakeServerWithSlowModuleList(t, 2*time.Second)

	start := time.Now()
	msg := c.listModules("someowner")()
	elapsed := time.Since(start)

	_, ok := msg.(errMsg)
	attest.True(t, ok, attest.Sprintf("expected a timeout to surface as errMsg, got %T: %v", msg, msg))
	attest.True(t, elapsed < time.Second, attest.Sprintf("expected the call to time out around rpcTimeout (50ms), took %v", elapsed))
}

// TestDocsErr_ClearedOnSuccess verifies a stale docsErr from a previous
// failed compile doesn't linger and shadow a subsequent successful one.
func TestDocsErr_ClearedOnSuccess(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)
	m.docsErr = fmt.Errorf("stale error from a previous failed compile")

	m2, _ := m.Update(docsMsg{files: &protoregistry.Files{}})
	m = m2.(model)

	attest.Equal(t, m.docsErr, nil)
}

// TestDocsMsg_SkippedMessagesSurfaceAStatusMessage verifies that when
// resolveRegistry reports skipped messages (e.g. legacy MessageSet), the
// user is told about it via the docs list's status message, rather than
// silently missing content with no explanation.
func TestDocsMsg_SkippedMessagesSurfaceAStatusMessage(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)

	_, cmd := m.Update(docsMsg{files: &protoregistry.Files{}, skipped: []string{"pkg.Legacy"}})
	attest.NotEqual(t, cmd, nil)

	_, cmd = m.Update(docsMsg{files: &protoregistry.Files{}})
	attest.Equal(t, cmd, nil)
}

// TestCompileDocs_RespectsCancellation verifies that compileDocs actually
// stops promptly when its context is cancelled -- as opposed to merely
// timing out eventually -- so an esc-to-cancel keypress can free up a
// long-running compile immediately instead of leaving it running in the
// background for the rest of its timeout budget.
func TestCompileDocs_RespectsCancellation(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	msg := c.compileDocs(ctx, "somecommit", nil)()
	elapsed := time.Since(start)

	_, ok := msg.(errMsg)
	attest.True(t, ok, attest.Sprintf("expected cancellation to surface as errMsg, got %T: %v", msg, msg))
	attest.True(t, elapsed < time.Second, attest.Sprintf("expected a near-instant return on an already-cancelled context, took %v", elapsed))
}

// TestBack_CancelsInFlightDocsCompile verifies that backing out of a commit
// whose docs are still compiling actually cancels the in-flight compile
// (frees the network connection and local compilation immediately) instead
// of leaving it running in the background for the rest of its timeout.
func TestBack_CancelsInFlightDocsCompile(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)
	m.state = modelStateBrowsingCommitContents
	m.currentOwner = "owner"
	m.currentModule = "module"
	m.loadingDocs = true

	cancelled := false
	m.docsCancel = func() { cancelled = true }

	m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = m2.(model)

	attest.True(t, cancelled, attest.Sprintf("expected backing out of the commit to cancel the in-flight docs compile"))
	attest.Equal(t, m.docsCancel, nil)
	attest.Equal(t, m.state, modelStateLoadingCommits)
}

// TestCompileDocs_CachesByCommitID verifies that a second compileDocs call
// for the same commit is served from cache instead of repeating the full
// graph-fetch+download+compile pipeline -- commits are immutable, so a
// cached result never goes stale. Backtracking to a previously-viewed
// commit should be instant rather than redoing real network+compute work.
func TestCompileDocs_CachesByCommitID(t *testing.T) {
	t.Parallel()

	c, graphHandler := startFakeServerForDocsCaching(t)
	files := []*modulev1.File{{
		Path:    "test.proto",
		Content: []byte("syntax = \"proto3\";\npackage test;\nmessage M {}\n"),
	}}

	msg1 := c.compileDocs(context.Background(), "commitA", files)()
	_, ok := msg1.(docsMsg)
	attest.True(t, ok, attest.Sprintf("expected the first compile to succeed, got %T: %v", msg1, msg1))
	attest.Equal(t, graphHandler.calls.Load(), int32(1))

	msg2 := c.compileDocs(context.Background(), "commitA", files)()
	_, ok = msg2.(docsMsg)
	attest.True(t, ok, attest.Sprintf("expected the second (cached) compile to succeed, got %T: %v", msg2, msg2))
	attest.Equal(t, graphHandler.calls.Load(), int32(1), attest.Sprintf("repeat compile for the same commit should be served from cache, not hit the network again"))

	msg3 := c.compileDocs(context.Background(), "commitB", files)()
	_, ok = msg3.(docsMsg)
	attest.True(t, ok, attest.Sprintf("expected a different commit's compile to succeed, got %T: %v", msg3, msg3))
	attest.Equal(t, graphHandler.calls.Load(), int32(2), attest.Sprintf("a different commit ID must not be served from another commit's cache entry"))
}

// TestDocsSearch_ActivateAndSubmit verifies the "/" search input activates
// only while a docs package is being viewed, that submitting a query closes
// the input, and that n/N (highlight navigation) don't panic once matches
// are set.
func TestDocsSearch_ActivateAndSubmit(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)
	m.state = modelStateBrowsingCommitFileContents
	m.activeCommitTab = commitTabDocs
	m.docsViewport.SetWidth(80)
	m.docsViewport.SetHeight(20)

	// First match on line 0 (visible without scrolling), second on line 39
	// (well past the 20-line-tall viewport, so reaching it via "n" only
	// works if the scroll position actually gets updated).
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = "padding line"
	}
	lines[0] = "findme first"
	lines[39] = "findme second"
	content := strings.Join(lines, "\n")
	m.docsViewport.SetContent(content)

	m2, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = m2.(model)
	attest.True(t, m.docsSearchActive, attest.Sprintf("expected \"/\" to activate the docs search input while viewing a package"))

	m.docsSearchInput.SetValue("findme")

	m2, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(model)
	attest.False(t, m.docsSearchActive, attest.Sprintf("search input should close after submitting a query"))
	attest.Equal(t, len(m.docsMatches), 2, attest.Sprintf("expected 2 matches, got %v", m.docsMatches))
	attest.Equal(t, m.docsMatchIdx, 0, attest.Sprintf("should jump to the first match on submit"))

	m2, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = m2.(model)
	attest.Equal(t, m.docsMatchIdx, 1, attest.Sprintf("n should advance to the second match"))
	attest.True(t, m.docsViewport.YOffset() > 0, attest.Sprintf("expected the viewport to scroll down to reveal the second match (line 39), stayed at yoffset=%d", m.docsViewport.YOffset()))

	m2, _ = m.Update(tea.KeyPressMsg{Code: 'N', Text: "N"})
	m = m2.(model)
	attest.Equal(t, m.docsMatchIdx, 0, attest.Sprintf("N should wrap back to the first match"))
}
