package main

import (
	"errors"
	"strings"
	"testing"

	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	"buf.build/go/protovalidate"
	"github.com/charmbracelet/x/ansi"
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

// TestDocsSearchMatches verifies matches are found case-insensitively and
// their byte offsets are relative to the ANSI-stripped text.
func TestDocsSearchMatches(t *testing.T) {
	t.Parallel()

	styled := "\x1b[1mHello\x1b[0m World, hello again"
	plain := "Hello World, hello again"

	matches := docsSearchMatches(styled, "hello")
	attest.Equal(t, len(matches), 2, attest.Sprintf("expected 2 case-insensitive matches, got %v", matches))
	for _, m := range matches {
		attest.Equal(t, len(m), 2)
		got := plain[m[0]:m[1]]
		attest.True(t, strings.EqualFold(got, "hello"), attest.Sprintf("match range %v should cover \"hello\", got %q", m, got))
	}

	attest.Equal(t, len(docsSearchMatches(styled, "")), 0, attest.Sprintf("empty query should produce no matches"))
	attest.Equal(t, len(docsSearchMatches(styled, "nonexistent")), 0)
}

// TestDocsMatchLine verifies the 0-indexed line number is computed by
// counting newlines in the ANSI-stripped text up to a byte offset.
//
// viewport.Model.SetHighlights/EnsureVisible could in principle do this
// scrolling (and highlighting) automatically, but was found via live
// testing against buf.build/svanburen/protobuf-conformance to compute the
// wrong line entirely for realistically complex, heavily and repeatedly
// re-styled content -- landing on an unrelated word many lines away from
// the actual match. Computing the target line directly and scrolling to it
// manually sidesteps that; see the Search/SearchNext/SearchPrev handling
// in main.go.
func TestDocsMatchLine(t *testing.T) {
	t.Parallel()

	content := "\x1b[1mline zero\x1b[0m\nline one\nline two has needle here\nline three"
	needleOffset := strings.Index(ansi.Strip(content), "needle")

	attest.Equal(t, docsMatchLine(content, 0), 0)
	attest.Equal(t, docsMatchLine(content, needleOffset), 2)
}
