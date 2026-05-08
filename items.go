package main

import (
	"fmt"
	"time"

	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
)

// relativeTime returns a human-readable relative time string for t.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
}

type module struct {
	underlying *modulev1.Module
	remote     string
	owner      string
}

// FilterValue implements [list.Item].
func (m *module) FilterValue() string {
	return m.underlying.Name
}

// Title implements [list.DefaultItem].
func (m *module) Title() string {
	var title string
	if m.underlying.Visibility == modulev1.ModuleVisibility_MODULE_VISIBILITY_PRIVATE {
		title += "󰎠"
	}
	title += m.underlying.Name
	if m.underlying.State == modulev1.ModuleState_MODULE_STATE_DEPRECATED {
		title += " (Deprecated)"
	}
	return title
}

// Description implements [list.DefaultItem].
func (m *module) Description() string {
	return m.underlying.Description
}

type commit struct {
	underlying *modulev1.Commit
	remote     string
	owner      string
	moduleName string
}

// FilterValue implements list.Item.
func (m *commit) FilterValue() string {
	return m.underlying.Id
}

// Title implements list.DefaultItem.
func (m *commit) Title() string {
	return m.underlying.Id
}

// Description implements list.DefaultItem.
func (m *commit) Description() string {
	t := m.underlying.CreateTime.AsTime()
	return fmt.Sprintf("%s (%s)", t.Format(time.Stamp), relativeTime(t))
}

type commitFile struct {
	underlying *modulev1.File
	remote     string
	owner      string
	moduleName string
	commitID   string
}

// FilterValue implements list.Item.
func (m *commitFile) FilterValue() string {
	return m.underlying.Path
}

// Title implements list.DefaultItem.
func (m *commitFile) Title() string {
	return m.underlying.Path
}

// Description implements list.DefaultItem.
// The description for a commit file is not shown.
func (m *commitFile) Description() string {
	return ""
}

type labelItem struct {
	underlying *modulev1.Label
	remote     string
	owner      string
	moduleName string
	isDefault  bool
}

// FilterValue implements list.Item.
func (l *labelItem) FilterValue() string {
	return l.underlying.Name
}

// Title implements list.DefaultItem.
func (l *labelItem) Title() string {
	if l.isDefault {
		return l.underlying.Name + " (default)"
	}
	return l.underlying.Name
}

// Description implements list.DefaultItem.
func (l *labelItem) Description() string {
	t := l.underlying.UpdateTime.AsTime()
	return fmt.Sprintf("%s · %s", l.underlying.CommitId[:12], relativeTime(t))
}
