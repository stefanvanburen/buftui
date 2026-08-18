package main

import (
	"strings"
	"testing"
	"time"

	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestShortenSourceControlURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "github commit URL is shortened to owner/repo@shortSHA",
			url:  "https://github.com/bufbuild/registry/commit/abc123def4567890fabc",
			want: "github.com/bufbuild/registry@abc123def456",
		},
		{
			name: "gitlab-style commits URL is shortened",
			url:  "https://gitlab.com/owner/repo/commits/deadbeefcafe1234",
			want: "gitlab.com/owner/repo@deadbeefcafe",
		},
		{
			name: "short ref is kept as-is",
			url:  "https://github.com/bufbuild/registry/commit/abc123",
			want: "github.com/bufbuild/registry@abc123",
		},
		{
			name: "non-hex ref longer than 12 chars is kept as-is",
			url:  "https://example.com/bufbuild/registry/commit/not-a-hex-sha-ref",
			want: "example.com/bufbuild/registry@not-a-hex-sha-ref",
		},
		{
			name: "unrecognized shape falls back to host",
			url:  "https://example.com/some/other/path",
			want: "example.com",
		},
		{
			name: "invalid URL is returned unchanged",
			url:  "not a url",
			want: "not a url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shortenSourceControlURL(tt.url)
			if got != tt.want {
				t.Errorf("shortenSourceControlURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestCommitDescription_SourceControlURL(t *testing.T) {
	t.Parallel()

	base := &commit{underlying: &modulev1.Commit{
		Id:         "abc123",
		CreateTime: timestamppb.New(time.Now()),
	}}
	descWithoutURL := base.Description()
	if strings.Contains(descWithoutURL, "@") {
		t.Errorf("description %q should not contain a source control marker when URL is unset", descWithoutURL)
	}

	withURL := &commit{underlying: &modulev1.Commit{
		Id:               "abc123",
		CreateTime:       timestamppb.New(time.Now()),
		SourceControlUrl: "https://github.com/bufbuild/registry/commit/abc123def4567890",
	}}
	descWithURL := withURL.Description()
	if !strings.Contains(descWithURL, "github.com/bufbuild/registry@abc123def456") {
		t.Errorf("description %q should contain the shortened source control URL", descWithURL)
	}
}
