package main

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// commitTab identifies which tab is active in the commit detail view.
type commitTab int

const (
	commitTabFiles  commitTab = iota
	commitTabLabels
	commitTabCount // sentinel for wrapping
)

func (t commitTab) String() string {
	switch t {
	case commitTabFiles:
		return "Files"
	case commitTabLabels:
		return "Labels"
	default:
		return ""
	}
}

var allCommitTabs = []commitTab{
	commitTabFiles,
	commitTabLabels,
}

// renderTabBar renders a horizontal tab bar with the active tab highlighted.
func renderTabBar(activeTab commitTab, isDark bool) string {
	lightDark := lipgloss.LightDark(isDark)
	inactiveColor := lightDark(lipgloss.Color("#777777"), lipgloss.Color("#999999"))

	parts := make([]string, len(allCommitTabs))
	for i, tab := range allCommitTabs {
		if tab == activeTab {
			parts[i] = lipgloss.NewStyle().
				Foreground(colorForeground).
				Bold(true).
				Padding(0, 1).
				Render(tab.String())
		} else {
			parts[i] = lipgloss.NewStyle().
				Foreground(inactiveColor).
				Padding(0, 1).
				Render(tab.String())
		}
	}
	return strings.Join(parts, "")
}
