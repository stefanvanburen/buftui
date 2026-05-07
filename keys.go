package main

import (
	"strings"

	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	"buf.build/go/protovalidate"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
)

// keyMap defines a set of keybindings. To work for help it must satisfy
// key.Map. It could also very easily be a map[string]key.Binding.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	Left     key.Binding
	Right    key.Binding
	Back     key.Binding
	Navigate key.Binding
	Enter    key.Binding
	Help     key.Binding
	Quit     key.Binding
	Browse   key.Binding
	Yank     key.Binding
	TabLeft  key.Binding
	TabRight key.Binding
}

var keys = keyMap{
	// TODO: Ideally we'd pull these from the viewing model KeyMap defaults
	// (e.g. m.fileViewport.KeyMap); for now just give some basics.
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "move up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "move down"),
	),
	Left: key.NewBinding(
		key.WithKeys("left", "h"),
		key.WithHelp("←/h", "go out"),
	),
	Right: key.NewBinding(
		key.WithKeys("right", "l"),
		key.WithHelp("→/l", "go in"),
	),
	Back: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back / quit"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "navigate"),
	),
	Navigate: key.NewBinding(
		key.WithKeys("g"),
		key.WithHelp("g", "navigate to owner/module"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "toggle help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	),
	Browse: key.NewBinding(
		key.WithKeys("o"),
		key.WithHelp("o", "open in browser"),
	),
	Yank: key.NewBinding(
		key.WithKeys("y"),
		key.WithHelp("y", "copy URL"),
	),
	TabLeft: key.NewBinding(
		key.WithKeys("["),
		key.WithHelp("[", "prev tab"),
	),
	TabRight: key.NewBinding(
		key.WithKeys("]"),
		key.WithHelp("]", "next tab"),
	),
}

func (m model) ShortHelp() []key.Binding {
	var shortHelp []key.Binding
	switch m.state {
	case modelStateBrowsingModules:
		// Can't go Left while browsing modules; already at the "top".
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Browse, keys.Yank}
		if len(m.currentModules) != 0 {
			// Can only go right when modules exist.
			shortHelp = append(shortHelp, keys.Right)
		}
	case modelStateBrowsingCommits:
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Back, keys.Yank}
		if len(m.currentCommits) != 0 {
			shortHelp = append(shortHelp, keys.Right)
		}
	case modelStateBrowsingCommitContents:
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Back, keys.TabLeft, keys.TabRight}
		switch m.activeCommitTab {
		case commitTabFiles:
			shortHelp = append(shortHelp, keys.Yank, keys.Right)
		case commitTabLabels:
			if len(m.currentLabels) > 0 {
				shortHelp = append(shortHelp, keys.Right)
			}
		}
	case modelStateBrowsingCommitFileContents:
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Back, keys.Yank, keys.TabLeft, keys.TabRight}
	case modelStateNavigating:
		shortHelp = []key.Binding{keys.Enter, keys.Back}
		if len(m.navigateInput.AvailableSuggestions()) > 0 {
			shortHelp = append(shortHelp,
				key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "accept")),
				key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑/↓", "cycle suggestions")),
			)
		}
	default:
		// In the other states, just show Help and Quit.
		return []key.Binding{keys.Help, keys.Quit}
	}
	// Always show the Help key.
	return append(shortHelp, keys.Help)
}

func (m model) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		m.ShortHelp(),
		{keys.Left, keys.Navigate, keys.Help, keys.Quit},
	}
}

func newNavigateInput() textinput.Model {
	input := textinput.New()
	input.Validate = func(inputStr string) error {
		// Try to parse as a complete reference first.
		if _, _, err := parseReference(inputStr); err == nil {
			return nil
		}
		// A slash indicates a partial reference being typed (e.g. "owner/"
		// or "owner/partialmodule"); don't show an error for intermediate states.
		if strings.Contains(inputStr, "/") {
			return nil
		}
		// Fall back to validating as an owner name.
		return protovalidate.Validate(&ownerv1.OwnerRef{
			Value: &ownerv1.OwnerRef_Name{Name: inputStr},
		})
	}
	input.ShowSuggestions = true
	input.Focus()
	input.Placeholder = "bufbuild/registry:main"
	return input
}
