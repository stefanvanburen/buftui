package main

import (
	"context"
	"encoding/base64"
	"fmt"

	"buf.build/gen/go/bufbuild/registry/connectrpc/go/buf/registry/module/v1/modulev1connect"
	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	"connectrpc.com/connect"
	tea "github.com/charmbracelet/bubbletea"
)

type client struct {
	moduleServiceClient   modulev1connect.ModuleServiceClient
	commitServiceClient   modulev1connect.CommitServiceClient
	downloadServiceClient modulev1connect.DownloadServiceClient
	resourceServiceClient modulev1connect.ResourceServiceClient
}

func newClient(httpClient connect.HTTPClient, remote, username, token string) *client {
	authInterceptor := newAuthInterceptor(username, token)
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
	}
}

type modulesMsg []*modulev1.Module

func (c *client) listModules(currentOwner string) tea.Cmd {
	return func() tea.Msg {
		request := connect.NewRequest(&modulev1.ListModulesRequest{
			PageSize: 50,
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
			return errMsg{fmt.Errorf("listing modules: %s", err)}
		}
		return modulesMsg(response.Msg.Modules)
	}
}

type commitsMsg []*modulev1.Commit

func (c *client) listCommits(currentOwner, currentModule string) tea.Cmd {
	return func() tea.Msg {
		request := connect.NewRequest(&modulev1.ListCommitsRequest{
			PageSize: 50,
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
			return errMsg{fmt.Errorf("getting commits: %s", err)}
		}
		return commitsMsg(response.Msg.Commits)
	}
}

type contentsMsg *modulev1.DownloadResponse_Content

func (c *client) getCommitContent(currentOwner, currentModule, commitName string) tea.Cmd {
	return func() tea.Msg {
		request := connect.NewRequest(&modulev1.DownloadRequest{
			Values: []*modulev1.DownloadRequest_Value{
				{
					ResourceRef: &modulev1.ResourceRef{
						Value: &modulev1.ResourceRef_Name_{
							Name: &modulev1.ResourceRef_Name{
								Owner:  currentOwner,
								Module: currentModule,
								Child: &modulev1.ResourceRef_Name_Ref{
									Ref: commitName,
								},
							},
						},
					},
				},
			},
		})
		response, err := c.downloadServiceClient.Download(context.Background(), request)
		if err != nil {
			return errMsg{fmt.Errorf("getting commit content: %s", err)}
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
			return errMsg{fmt.Errorf("getting resource: %s", err)}
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

// newAuthInterceptor creates a client-only interceptor for adding
// authentication to requests.
func newAuthInterceptor(username, token string) connect.UnaryInterceptorFunc {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return connect.UnaryFunc(func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {
			if req.Spec().IsClient {
				req.Header().Set(
					"Authorization",
					"Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+token)),
				)
			} else {
				return nil, fmt.Errorf("auth interceptor is a client-only interceptor")
			}
			return next(ctx, req)
		})
	})
}
