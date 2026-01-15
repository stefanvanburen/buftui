package main

import (
	"connectrpc.com/connect"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"buf.build/gen/go/bufbuild/registry/connectrpc/go/buf/registry/module/v1/modulev1connect"
	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	"github.com/charmbracelet/bubbles/v2/help"
	"github.com/charmbracelet/bubbles/v2/list"
	"github.com/charmbracelet/bubbles/v2/spinner"
	"github.com/charmbracelet/bubbles/v2/viewport"
	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"go.akshayshah.org/attest"
	"go.akshayshah.org/memhttp"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeModuleServiceHandler implements the ModuleService for testing.
type fakeModuleServiceHandler struct {
	modulev1connect.UnimplementedModuleServiceHandler
}

func (f *fakeModuleServiceHandler) ListModules(
	ctx context.Context,
	req *connect.Request[modulev1.ListModulesRequest],
) (*connect.Response[modulev1.ListModulesResponse], error) {
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

// startFakeServer creates an in-memory Buf registry service and returns a client.
func startFakeServer(t *testing.T) *client {
	t.Helper()

	// Setup Connect handlers
	mux := http.NewServeMux()
	mux.Handle(modulev1connect.NewModuleServiceHandler(&fakeModuleServiceHandler{}))
	mux.Handle(modulev1connect.NewCommitServiceHandler(&fakeCommitServiceHandler{}))
	mux.Handle(modulev1connect.NewDownloadServiceHandler(&fakeDownloadServiceHandler{}))
	mux.Handle(modulev1connect.NewResourceServiceHandler(&fakeResourceServiceHandler{}))

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
	}
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

	return model{
		state:            modelStateNavigating,
		spinner:          spinner.New(spinner.WithSpinner(spinner.Dot)),
		client:           c,
		help:             help.New(),
		keys:             keys,
		currentReference: nil,
		navigateInput:    newNavigateInput(false),
		remote:           "buf.build",
		fileViewport:     viewport.New(),

		moduleList:      moduleList,
		commitList:      commitList,
		commitFilesList: commitFilesList,
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

// TestNavigateToOwner tests navigating to an owner and loading modules.
func TestNavigateToOwner(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	defer tm.Quit()

	// Type an owner name
	tm.Type("bufbuild")

	// Press enter to navigate (send KeyPressMsg with Enter key)
	tm.Send(tea.KeyPressMsg(tea.Key{Code: '\r'}))

	// Wait for modules to load
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		output := string(bts)
		return strings.Contains(output, "Modules (Owner: bufbuild)")
	}, teatest.WithDuration(2*time.Second))
}

// TestNavigateWithReference tests navigating with a specific reference.
func TestNavigateWithReference(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	defer tm.Quit()

	// Type a reference
	tm.Type("bufbuild/registry")

	// Press enter to navigate
	tm.Send(tea.KeyPressMsg(tea.Key{Code: '\r'}))

	// Wait for commits to load
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		output := string(bts)
		// After loading a reference, it should navigate to commits
		return strings.Contains(output, "Commits") || strings.Contains(output, "Loading")
	}, teatest.WithDuration(2*time.Second))
}

// TestQuitWithEscapeKey tests that Esc key quits the application.
func TestQuitWithEscapeKey(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))

	// Wait for initial render
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		output := string(bts)
		return strings.Contains(output, "Navigate to owner")
	}, teatest.WithDuration(2*time.Second))

	// Send quit key (Esc)
	tm.Send(tea.KeyPressMsg(tea.Key{Code: 27})) // ESC

	// Wait for program to finish
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestWindowResizing tests that the model handles window resize messages.
func TestWindowResizing(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	defer tm.Quit()

	// Wait for initial render
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		output := string(bts)
		return strings.Contains(output, "Navigate to owner")
	}, teatest.WithDuration(1*time.Second))

	// Send resize message
	tm.Send(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Wait a bit for the resize to be processed
	time.Sleep(100 * time.Millisecond)

	// Should still show the navigation interface
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		output := string(bts)
		return strings.Contains(output, "Navigate to owner")
	}, teatest.WithDuration(1*time.Second))
}

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
	attest.True(t, len(commits) > 0, attest.Sprintf("expected commits, got %d", len(commits)))
	attest.Equal(t, commits[0].Id, "abc123def456")
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
			for _, text := range tt.contains {
				attest.True(t, strings.Contains(view, text), attest.Sprintf("view should contain %q", text))
			}
		})
	}
}
