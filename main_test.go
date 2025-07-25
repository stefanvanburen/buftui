package main

import (
	"errors"
	"strings"
	"testing"

	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	"buf.build/go/protovalidate"
	"go.akshayshah.org/attest"
	"google.golang.org/protobuf/testing/protocmp"
)

func Test_parseReference(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reference           string
		wantRemote          string
		wantResourceRef     *modulev1.ResourceRef_Name
		wantError           bool
		wantValidationError bool
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
		{
			reference: strings.Repeat("a", 33) + "/abc",
			// Owner name too long, based on protovalidate rules.
			wantError:           true,
			wantValidationError: true,
		},
		{
			reference: "abc/a",
			// Repository name too short, based on protovalidate rules.
			wantError:           true,
			wantValidationError: true,
		},
	} {
		t.Run("reference: "+tc.reference, func(t *testing.T) {
			t.Parallel()
			gotRemote, gotResourceRef, gotErr := parseReference(tc.reference)
			if tc.wantError {
				attest.Error(t, gotErr)
				if tc.wantValidationError {
					err := &protovalidate.ValidationError{}
					attest.True(t, errors.As(gotErr, &err))
				}
			} else {
				attest.Equal(t, gotRemote, tc.wantRemote)
				attest.Equal(t, gotResourceRef, tc.wantResourceRef, attest.Cmp(protocmp.Transform()))
			}
		})
	}
}
