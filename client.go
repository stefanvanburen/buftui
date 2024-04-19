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

func newClient(httpClient connect.HTTPClient, username, token string) *client {
	return &client{
		httpClient: httpClient,
		interceptors: []connect.Interceptor{
			newAuthInterceptor(username, token),
		},
	}
}

type client struct {
	httpClient   connect.HTTPClient
	interceptors []connect.Interceptor
}

type modulesMsg []*modulev1.Module

func (c *client) getModules(remote, currentOwner string) tea.Cmd {
	return func() tea.Msg {
		moduleServiceClient := modulev1connect.NewModuleServiceClient(
			c.httpClient,
			"https://"+remote,
			connect.WithInterceptors(c.interceptors...),
		)
		request := connect.NewRequest(&modulev1.ListModulesRequest{
			OwnerRefs: []*ownerv1.OwnerRef{
				{
					Value: &ownerv1.OwnerRef_Name{
						Name: currentOwner,
					},
				},
			},
		})
		response, err := moduleServiceClient.ListModules(context.Background(), request)
		if err != nil {
			return errMsg{fmt.Errorf("listing modules: %s", err)}
		}
		return modulesMsg(response.Msg.Modules)
	}
}

type commitsMsg []*modulev1.Commit

func (c *client) listCommits(remote, currentOwner, currentModule string) tea.Cmd {
	return func() tea.Msg {
		commitServiceClient := modulev1connect.NewCommitServiceClient(
			c.httpClient,
			"https://"+remote,
			connect.WithInterceptors(c.interceptors...),
		)
		request := connect.NewRequest(&modulev1.ListCommitsRequest{
			ResourceRef: &modulev1.ResourceRef{
				Value: &modulev1.ResourceRef_Name_{
					Name: &modulev1.ResourceRef_Name{
						Owner:  currentOwner,
						Module: currentModule,
					},
				},
			},
		})
		response, err := commitServiceClient.ListCommits(context.Background(), request)
		if err != nil {
			return errMsg{fmt.Errorf("getting commits: %s", err)}
		}
		return commitsMsg(response.Msg.Commits)
	}
}

type contentsMsg *modulev1.DownloadResponse_Content

func (c *client) getCommitContent(remote, currentOwner, currentModule, commitName string) tea.Cmd {
	return func() tea.Msg {
		downloadServiceClient := modulev1connect.NewDownloadServiceClient(
			c.httpClient,
			"https://"+remote,
			connect.WithInterceptors(c.interceptors...),
		)
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
		response, err := downloadServiceClient.Download(context.Background(), request)
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

func (c *client) getResource(remote string, resourceName *modulev1.ResourceRef_Name) tea.Cmd {
	return func() tea.Msg {
		resourceServiceClient := modulev1connect.NewResourceServiceClient(
			c.httpClient,
			"https://"+remote,
			connect.WithInterceptors(c.interceptors...),
		)
		request := connect.NewRequest(&modulev1.GetResourcesRequest{
			ResourceRefs: []*modulev1.ResourceRef{
				{
					Value: &modulev1.ResourceRef_Name_{
						Name: resourceName,
					},
				},
			},
		})
		response, err := resourceServiceClient.GetResources(context.Background(), request)
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
					"Basic "+base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", username, token))),
				)
			} else {
				return nil, fmt.Errorf("auth interceptor is a client-only interceptor")
			}
			return next(ctx, req)
		})
	})
}
