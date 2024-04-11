package main

import (
	"testing"
	"time"

	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	"go.akshayshah.org/attest"
	"google.golang.org/protobuf/testing/protocmp"
)

func Test_parseReference(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reference       string
		wantRemote      string
		wantResourceRef *modulev1.ResourceRef_Name
		wantError       bool
	}{
		{
			reference:  "bufbuild/registry",
			wantRemote: "",
			wantResourceRef: &modulev1.ResourceRef_Name{
				Owner:  "bufbuild",
				Module: "registry",
			},
			wantError: false,
		},
		{
			reference:  "buf.build/bufbuild/registry",
			wantRemote: "buf.build",
			wantResourceRef: &modulev1.ResourceRef_Name{
				Owner:  "bufbuild",
				Module: "registry",
			},
			wantError: false,
		},
		{
			reference:  "buf.build/bufbuild/registry:569d290ee4cc4ed38499daf2c4fe39e6",
			wantRemote: "buf.build",
			wantResourceRef: &modulev1.ResourceRef_Name{
				Owner:  "bufbuild",
				Module: "registry",
				Child: &modulev1.ResourceRef_Name_Ref{
					Ref: "569d290ee4cc4ed38499daf2c4fe39e6",
				},
			},
			wantError: false,
		},
		{
			// We don't support URL schemes.
			reference: "https://buf.build/bufbuild/registry:569d290ee4cc4ed38499daf2c4fe39e6",
			wantError: true,
		},
		{
			reference: "bufbuild/bufbuild/registry:569d290ee4cc4ed38499daf2c4fe39e6",
			// TODO: We don't currently validate the hostname.
			wantRemote: "bufbuild",
			wantResourceRef: &modulev1.ResourceRef_Name{
				Owner:  "bufbuild",
				Module: "registry",
				Child: &modulev1.ResourceRef_Name_Ref{
					Ref: "569d290ee4cc4ed38499daf2c4fe39e6",
				},
			},
			wantError: false,
		},
	} {
		gotRemote, gotResourceRef, gotErr := parseReference(tc.reference)
		if tc.wantError {
			attest.Error(t, gotErr)
		} else {
			attest.Equal(t, gotRemote, tc.wantRemote)
			attest.Equal(t, gotResourceRef, tc.wantResourceRef, attest.Cmp(protocmp.Transform()))
		}
	}
}

func Test_formatTimeAgo(t *testing.T) {
	t.Parallel()
	now := time.Date(2000, time.May, 31, 16, 39, 0, 0, time.Local)
	for _, tc := range []struct {
		timestamp time.Time
		want      string
	}{
		{
			timestamp: now.Add(time.Second),
			want:      "in the future",
		},
		{
			timestamp: now,
			want:      "now",
		},
		{
			timestamp: now.Add(-time.Second),
			want:      "a few seconds ago",
		},
		{
			timestamp: now.Add(-529600 * time.Minute),
			want:      "last year",
		},
		{
			timestamp: now.Add(-2 * 529600 * time.Minute),
			want:      "2 years ago",
		},
		{
			timestamp: now.Add(-40 * 24 * time.Hour),
			want:      "last month",
		},
		{
			timestamp: now.Add(-70 * 24 * time.Hour),
			want:      "2 months ago",
		},
		{
			timestamp: now.Add(-39 * time.Minute),
			want:      "39 minutes ago",
		},
		{
			timestamp: now.Add(-5 * time.Hour),
			want:      "5 hours ago",
		},
	} {
		got := formatTimeAgo(now, tc.timestamp)
		attest.Equal(t, got, tc.want)
	}
}
