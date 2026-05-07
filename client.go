package main

import (
	"context"
	"fmt"

	"buf.build/gen/go/bufbuild/registry/connectrpc/go/buf/registry/module/v1/modulev1connect"
	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	tea "charm.land/bubbletea/v2"
	"connectrpc.com/connect"
)

const pageSize = 250

type client struct {
	moduleServiceClient   modulev1connect.ModuleServiceClient
	commitServiceClient   modulev1connect.CommitServiceClient
	downloadServiceClient modulev1connect.DownloadServiceClient
	resourceServiceClient modulev1connect.ResourceServiceClient
	labelServiceClient    modulev1connect.LabelServiceClient
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
	}
}

type modulesMsg []*modulev1.Module

type labelsMsg []*modulev1.Label

func (c *client) listModules(currentOwner string) tea.Cmd {
	return func() tea.Msg {
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
			response, err := c.moduleServiceClient.ListModules(context.Background(), request)
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
		response, err := c.commitServiceClient.ListCommits(context.Background(), request)
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
		response, err := c.commitServiceClient.ListCommits(context.Background(), request)
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
		response, err := c.downloadServiceClient.Download(context.Background(), request)
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
		request := connect.NewRequest(&modulev1.GetResourcesRequest{
			ResourceRefs: []*modulev1.ResourceRef{
				{
					Value: &modulev1.ResourceRef_Name_{
						Name: resourceName,
					},
				},
			},
		})
		response, err := c.resourceServiceClient.GetResources(context.Background(), request)
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
			response, err := c.labelServiceClient.ListLabels(context.Background(), request)
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
		response, err := c.labelServiceClient.ListLabels(context.Background(), request)
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
		request := connect.NewRequest(&modulev1.ListModulesRequest{
			PageSize: pageSize,
			OwnerRefs: []*ownerv1.OwnerRef{{
				Value: &ownerv1.OwnerRef_Name{Name: owner},
			}},
		})
		response, err := c.moduleServiceClient.ListModules(context.Background(), request)
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
