package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"buf.build/gen/go/bufbuild/registry/connectrpc/go/buf/registry/module/v1/modulev1connect"
	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	tea "charm.land/bubbletea/v2"
	"connectrpc.com/connect"
	"github.com/bufbuild/protocompile/experimental/fdp"
	"github.com/bufbuild/protocompile/experimental/incremental"
	"github.com/bufbuild/protocompile/experimental/incremental/queries"
	"github.com/bufbuild/protocompile/experimental/ir"
	"github.com/bufbuild/protocompile/experimental/source"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

const pageSize = 250

// rpcTimeout bounds a single simple RPC call (listing modules/commits/labels,
// fetching a resource or commit content). Every BSR RPC call used to run
// under context.Background() with no deadline at all, so a slow or hanging
// backend response could block the whole app forever with no way to
// recover. Var, not const, so tests can shrink it to exercise the timeout
// path without waiting tens of seconds.
var rpcTimeout = 30 * time.Second

// compileDocsTimeout bounds the whole compileDocs pipeline (dependency graph
// fetch, dependency download, and local compilation combined), which does
// more work than a single simple RPC call and so gets a longer budget of
// its own rather than reusing rpcTimeout per step.
var compileDocsTimeout = 2 * time.Minute

type client struct {
	moduleServiceClient   modulev1connect.ModuleServiceClient
	commitServiceClient   modulev1connect.CommitServiceClient
	downloadServiceClient modulev1connect.DownloadServiceClient
	resourceServiceClient modulev1connect.ResourceServiceClient
	labelServiceClient    modulev1connect.LabelServiceClient
	graphServiceClient    modulev1connect.GraphServiceClient
}

func newClient(httpClient connect.HTTPClient, remote, token string) *client {
	authInterceptor := newAuthInterceptor(token)
	options := connect.WithClientOptions(
		connect.WithInterceptors(authInterceptor),
		connect.WithHTTPGet(),
	)
	address := "https://" + remote
	return &client{
		moduleServiceClient:   modulev1connect.NewModuleServiceClient(httpClient, address, options),
		commitServiceClient:   modulev1connect.NewCommitServiceClient(httpClient, address, options),
		downloadServiceClient: modulev1connect.NewDownloadServiceClient(httpClient, address, options),
		resourceServiceClient: modulev1connect.NewResourceServiceClient(httpClient, address, options),
		labelServiceClient:    modulev1connect.NewLabelServiceClient(httpClient, address, options),
		graphServiceClient:    modulev1connect.NewGraphServiceClient(httpClient, address, options),
	}
}

type modulesMsg []*modulev1.Module

type labelsMsg []*modulev1.Label

type docsMsg *protoregistry.Files

func (c *client) listModules(currentOwner string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		var allModules []*modulev1.Module
		pageToken := ""
		for {
			request := connect.NewRequest(&modulev1.ListModulesRequest{
				PageSize:  pageSize,
				PageToken: pageToken,
				OwnerRefs: []*ownerv1.OwnerRef{
					{
						Value: &ownerv1.OwnerRef_Name{
							Name: currentOwner,
						},
					},
				},
			})
			response, err := c.moduleServiceClient.ListModules(ctx, request)
			if err != nil {
				return errMsg{fmt.Errorf("listing modules: %w", err)}
			}
			allModules = append(allModules, response.Msg.Modules...)
			if response.Msg.NextPageToken == "" {
				break
			}
			pageToken = response.Msg.NextPageToken
		}
		return modulesMsg(allModules)
	}
}

type commitsMsg struct {
	commits       []*modulev1.Commit
	nextPageToken string
}

type moreCommitsMsg commitsMsg

func (c *client) listCommits(currentOwner, currentModule string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		request := connect.NewRequest(&modulev1.ListCommitsRequest{
			PageSize: pageSize,
			ResourceRef: &modulev1.ResourceRef{
				Value: &modulev1.ResourceRef_Name_{
					Name: &modulev1.ResourceRef_Name{
						Owner:  currentOwner,
						Module: currentModule,
					},
				},
			},
		})
		response, err := c.commitServiceClient.ListCommits(ctx, request)
		if err != nil {
			return errMsg{fmt.Errorf("getting commits: %w", err)}
		}
		return commitsMsg{
			commits:       response.Msg.Commits,
			nextPageToken: response.Msg.NextPageToken,
		}
	}
}

func (c *client) listMoreCommits(currentOwner, currentModule, pageToken string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		request := connect.NewRequest(&modulev1.ListCommitsRequest{
			PageSize:  pageSize,
			PageToken: pageToken,
			ResourceRef: &modulev1.ResourceRef{
				Value: &modulev1.ResourceRef_Name_{
					Name: &modulev1.ResourceRef_Name{
						Owner:  currentOwner,
						Module: currentModule,
					},
				},
			},
		})
		response, err := c.commitServiceClient.ListCommits(ctx, request)
		if err != nil {
			return errMsg{fmt.Errorf("getting more commits: %w", err)}
		}
		return moreCommitsMsg{
			commits:       response.Msg.Commits,
			nextPageToken: response.Msg.NextPageToken,
		}
	}
}

type contentsMsg *modulev1.DownloadResponse_Content

func (c *client) getCommitContent(commitID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		request := connect.NewRequest(&modulev1.DownloadRequest{
			Values: []*modulev1.DownloadRequest_Value{
				{
					ResourceRef: &modulev1.ResourceRef{
						Value: &modulev1.ResourceRef_Id{
							Id: commitID,
						},
					},
				},
			},
		})
		response, err := c.downloadServiceClient.Download(ctx, request)
		if err != nil {
			return errMsg{fmt.Errorf("getting commit content: %w", err)}
		}
		if len(response.Msg.Contents) != 1 {
			return errMsg{fmt.Errorf("requested 1 commit contents, got %v", len(response.Msg.Contents))}
		}
		return contentsMsg(response.Msg.Contents[0])
	}
}

type resourceMsg struct {
	// Avoid races with other commands by ensuring that we return the
	// request with the response.
	requestedResource *modulev1.ResourceRef_Name
	retrievedResource *modulev1.Resource
}

func (c *client) getResource(resourceName *modulev1.ResourceRef_Name) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		request := connect.NewRequest(&modulev1.GetResourcesRequest{
			ResourceRefs: []*modulev1.ResourceRef{
				{
					Value: &modulev1.ResourceRef_Name_{
						Name: resourceName,
					},
				},
			},
		})
		response, err := c.resourceServiceClient.GetResources(ctx, request)
		if err != nil {
			return errMsg{fmt.Errorf("getting resource: %w", err)}
		}
		if len(response.Msg.Resources) != 1 {
			return errMsg{fmt.Errorf("requested 1 resource, got %v", len(response.Msg.Resources))}
		}
		return resourceMsg{
			requestedResource: resourceName,
			retrievedResource: response.Msg.Resources[0],
		}
	}
}

func (c *client) listLabels(owner, module string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		var allLabels []*modulev1.Label
		pageToken := ""
		for {
			request := connect.NewRequest(&modulev1.ListLabelsRequest{
				PageSize:  pageSize,
				PageToken: pageToken,
				ResourceRef: &modulev1.ResourceRef{
					Value: &modulev1.ResourceRef_Name_{
						Name: &modulev1.ResourceRef_Name{
							Owner:  owner,
							Module: module,
						},
					},
				},
				ArchiveFilter: modulev1.ListLabelsRequest_ARCHIVE_FILTER_UNARCHIVED_ONLY,
			})
			response, err := c.labelServiceClient.ListLabels(ctx, request)
			if err != nil {
				return errMsg{fmt.Errorf("listing labels: %w", err)}
			}
			allLabels = append(allLabels, response.Msg.Labels...)
			if response.Msg.NextPageToken == "" {
				break
			}
			pageToken = response.Msg.NextPageToken
		}
		return labelsMsg(allLabels)
	}
}

func (c *client) fetchLabelSuggestions(owner, module string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		request := connect.NewRequest(&modulev1.ListLabelsRequest{
			PageSize: pageSize,
			ResourceRef: &modulev1.ResourceRef{
				Value: &modulev1.ResourceRef_Name_{
					Name: &modulev1.ResourceRef_Name{
						Owner:  owner,
						Module: module,
					},
				},
			},
			ArchiveFilter: modulev1.ListLabelsRequest_ARCHIVE_FILTER_UNARCHIVED_ONLY,
		})
		response, err := c.labelServiceClient.ListLabels(ctx, request)
		if err != nil {
			// Suggestions are best-effort; ignore errors.
			return navigateSuggestionsMsg(nil)
		}
		suggestions := make([]string, len(response.Msg.Labels))
		for i, label := range response.Msg.Labels {
			suggestions[i] = owner + "/" + module + ":" + label.Name
		}
		return navigateSuggestionsMsg(suggestions)
	}
}

func (c *client) fetchModuleSuggestions(owner string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		request := connect.NewRequest(&modulev1.ListModulesRequest{
			PageSize: pageSize,
			OwnerRefs: []*ownerv1.OwnerRef{{
				Value: &ownerv1.OwnerRef_Name{Name: owner},
			}},
		})
		response, err := c.moduleServiceClient.ListModules(ctx, request)
		if err != nil {
			// Suggestions are best-effort; ignore errors.
			return navigateSuggestionsMsg(nil)
		}
		suggestions := make([]string, len(response.Msg.Modules))
		for i, mod := range response.Msg.Modules {
			suggestions[i] = owner + "/" + mod.Name
		}
		return navigateSuggestionsMsg(suggestions)
	}
}

// compileDocs fetches transitive dependencies via the graph service, downloads
// their proto files, and compiles everything using the experimental incremental
// compiler from protocompile.
func (c *client) compileDocs(commitID string, currentFiles []*modulev1.File) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), compileDocsTimeout)
		defer cancel()
		// 1. Get the full transitive dependency graph.
		graphResp, err := c.graphServiceClient.GetGraph(ctx, connect.NewRequest(&modulev1.GetGraphRequest{
			ResourceRefs: []*modulev1.ResourceRef{{
				Value: &modulev1.ResourceRef_Id{Id: commitID},
			}},
		}))
		if err != nil {
			return errMsg{fmt.Errorf("getting dependency graph: %w", err)}
		}

		// 2. Collect dep commit IDs (everything in the graph except the current commit).
		var depCommitIDs []string
		for _, commit := range graphResp.Msg.Graph.Commits {
			if commit.Id != commitID {
				depCommitIDs = append(depCommitIDs, commit.Id)
			}
		}

		// 3. Seed the source map from the current module's proto files.
		fileMap := source.NewMap(nil)
		for _, f := range currentFiles {
			if strings.HasSuffix(f.Path, ".proto") {
				fileMap.Add(f.Path, string(f.Content))
			}
		}

		// 4. Batch-download all dep proto files in a single request.
		if len(depCommitIDs) > 0 {
			values := make([]*modulev1.DownloadRequest_Value, len(depCommitIDs))
			for i, id := range depCommitIDs {
				values[i] = &modulev1.DownloadRequest_Value{
					ResourceRef: &modulev1.ResourceRef{
						Value: &modulev1.ResourceRef_Id{Id: id},
					},
					FileTypes: []modulev1.FileType{modulev1.FileType_FILE_TYPE_PROTO},
				}
			}
			dlResp, err := c.downloadServiceClient.Download(ctx, connect.NewRequest(&modulev1.DownloadRequest{
				Values: values,
			}))
			if err != nil {
				return errMsg{fmt.Errorf("downloading dependencies: %w", err)}
			}
			for _, content := range dlResp.Msg.Contents {
				for _, f := range content.Files {
					if strings.HasSuffix(f.Path, ".proto") {
						fileMap.Add(f.Path, string(f.Content))
					}
				}
			}
		}

		// 5. Build the opener: WKTs first, then module files.
		opener := &source.Openers{source.WKTs(), fileMap}

		// 6. Compile main module proto files using the experimental incremental compiler.
		session := &ir.Session{}
		executor := incremental.New()
		irQueries := make([]incremental.Query[*ir.File], 0, len(currentFiles))
		for _, f := range currentFiles {
			if strings.HasSuffix(f.Path, ".proto") {
				irQueries = append(irQueries, queries.IR{
					Opener:  opener,
					Session: session,
					Path:    f.Path,
				})
			}
		}
		irResults, _, err := incremental.Run(ctx, executor, irQueries...)
		if err != nil {
			return errMsg{fmt.Errorf("compiling protos: %w", err)}
		}
		irFiles := make([]*ir.File, 0, len(irResults))
		for _, r := range irResults {
			if r.Fatal != nil {
				return errMsg{fmt.Errorf("compiling protos: %w", r.Fatal)}
			}
			irFiles = append(irFiles, r.Value)
		}

		// 7. Convert IR files to a FileDescriptorSet (includes all deps except WKTs),
		// with source code info for comments.
		fdsBytes, err := fdp.DescriptorSetBytes(irFiles, fdp.IncludeSourceCodeInfo(true))
		if err != nil {
			return errMsg{fmt.Errorf("generating file descriptors: %w", err)}
		}
		// 8. Build a registry, re-resolving custom options against the
		// descriptor set's own extension declarations along the way.
		regFiles, err := resolveRegistry(fdsBytes)
		if err != nil {
			return errMsg{err}
		}
		return docsMsg(regFiles)
	}
}

// resolveRegistry builds a *protoregistry.Files from a marshaled
// FileDescriptorSet, re-resolving custom options (e.g. google.api.http,
// buf.validate.field) against the descriptor set's own extension
// declarations. A plain proto.Unmarshal only resolves extensions whose Go
// package happens to be statically linked into this binary; since buftui
// browses arbitrary third-party schemas, most custom options would
// otherwise be silently dropped into unknown fields and never render.
func resolveRegistry(fdsBytes []byte) (*protoregistry.Files, error) {
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(fdsBytes, &fds); err != nil {
		return nil, fmt.Errorf("unmarshalling file descriptors: %w", err)
	}
	stripMessageSets(&fds)
	// WKT deps are resolved from the global registry.
	regFiles, err := protodesc.NewFiles(&fds)
	if err != nil {
		return nil, fmt.Errorf("building file registry: %w", err)
	}

	var resolvedFDS descriptorpb.FileDescriptorSet
	unmarshalOpts := proto.UnmarshalOptions{Resolver: dynamicpb.NewTypes(regFiles)}
	if err := unmarshalOpts.Unmarshal(fdsBytes, &resolvedFDS); err != nil {
		return nil, fmt.Errorf("resolving custom options: %w", err)
	}
	stripMessageSets(&resolvedFDS)
	resolvedFiles, err := protodesc.NewFiles(&resolvedFDS)
	if err != nil {
		return nil, fmt.Errorf("building file registry with resolved options: %w", err)
	}
	return resolvedFiles, nil
}

// stripMessageSets removes, in place, any message using the legacy proto1
// MessageSet wire format ("option message_set_wire_format = true;") along
// with any now-dangling "extend" declarations against it. protodesc.NewFiles
// refuses to build a registry containing one at all -- MessageSet support
// was dropped from protobuf-go's default build, gated behind an unstable,
// explicitly not-compatibility-covered build tag -- so without this, one
// such message anywhere in a module (e.g. a legacy conformance-test fixture)
// would fail the whole registry and leave every other type undocumented.
func stripMessageSets(fds *descriptorpb.FileDescriptorSet) {
	removed := make(map[string]bool)

	var collect func(prefix string, msgs []*descriptorpb.DescriptorProto)
	collect = func(prefix string, msgs []*descriptorpb.DescriptorProto) {
		for _, m := range msgs {
			full := prefix + "." + m.GetName()
			if m.GetOptions().GetMessageSetWireFormat() {
				removed[full] = true
			}
			collect(full, m.GetNestedType())
		}
	}
	for _, f := range fds.GetFile() {
		prefix := ""
		if pkg := f.GetPackage(); pkg != "" {
			prefix = "." + pkg
		}
		collect(prefix, f.GetMessageType())
	}
	if len(removed) == 0 {
		return
	}

	pruneExtensions := func(exts []*descriptorpb.FieldDescriptorProto) []*descriptorpb.FieldDescriptorProto {
		kept := make([]*descriptorpb.FieldDescriptorProto, 0, len(exts))
		for _, x := range exts {
			if !removed[x.GetExtendee()] {
				kept = append(kept, x)
			}
		}
		return kept
	}
	var pruneMessages func(msgs []*descriptorpb.DescriptorProto, prefix string) []*descriptorpb.DescriptorProto
	pruneMessages = func(msgs []*descriptorpb.DescriptorProto, prefix string) []*descriptorpb.DescriptorProto {
		kept := make([]*descriptorpb.DescriptorProto, 0, len(msgs))
		for _, m := range msgs {
			full := prefix + "." + m.GetName()
			if removed[full] {
				continue
			}
			m.NestedType = pruneMessages(m.GetNestedType(), full)
			m.Extension = pruneExtensions(m.GetExtension())
			kept = append(kept, m)
		}
		return kept
	}
	for _, f := range fds.GetFile() {
		prefix := ""
		if pkg := f.GetPackage(); pkg != "" {
			prefix = "." + pkg
		}
		f.MessageType = pruneMessages(f.GetMessageType(), prefix)
		f.Extension = pruneExtensions(f.GetExtension())
	}
}

// newAuthInterceptor creates a client-only interceptor for adding authentication to requests.
func newAuthInterceptor(token string) connect.UnaryInterceptorFunc {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return connect.UnaryFunc(func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {
			if !req.Spec().IsClient {
				return nil, fmt.Errorf("auth interceptor is a client-only interceptor")
			}
			if token != "" {
				req.Header().Set("Authorization", "Bearer "+token)
			}
			return next(ctx, req)
		})
	})
}
