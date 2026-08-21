package main

import (
	"testing"

	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"go.vanburen.xyz/ok"
)

// TestShortHelp_BrowseSCM_GatedOnSourceControlURL verifies the "open source
// commit" binding is only advertised (and only actionable) when the selected
// commit actually has a SourceControlUrl -- BSR commits without one (the
// common case) must not show a dead keybinding or attempt to open an empty
// URL.
func TestShortHelp_BrowseSCM_GatedOnSourceControlURL(t *testing.T) {
	t.Parallel()

	c := startFakeServer(t)
	m := newTestModel(c)
	m.state = modelStateBrowsingCommits

	withoutURL := &commit{underlying: &modulev1.Commit{Id: "abc123"}}
	withURL := &commit{underlying: &modulev1.Commit{
		Id:               "def456",
		SourceControlUrl: "https://github.com/bufbuild/registry-proto/commit/abcdef1234567890",
	}}
	m.currentCommits = []*modulev1.Commit{withoutURL.underlying, withURL.underlying}

	// No SourceControlUrl: the binding must not appear in help, and
	// pressing "O" must be a no-op (no crash, no state change, no error).
	m.commitList.SetItems([]list.Item{withoutURL})
	for _, b := range m.ShortHelp() {
		ok.NotEqual(t, b.Help().Key, keys.BrowseSCM.Help().Key)
	}
	m2, cmd := m.Update(tea.KeyPressMsg{Code: 'O', Text: "O"})
	m = m2.(model)
	ok.True(t, cmd == nil, ok.Sprintf("expected no command"))
	ok.Equal(t, m.err, nil)
	ok.Equal(t, m.state, modelStateBrowsingCommits)

	// With a SourceControlUrl: the binding is advertised in help.
	m.commitList.SetItems([]list.Item{withURL})
	found := false
	for _, b := range m.ShortHelp() {
		if b.Help().Key == keys.BrowseSCM.Help().Key {
			found = true
		}
	}
	ok.True(t, found, ok.Sprintf("expected %q in short help when commit has a SourceControlUrl", keys.BrowseSCM.Help().Key))
}
