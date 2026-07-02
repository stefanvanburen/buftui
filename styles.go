package main

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
)

const (
	bufBlue   = "#0e5df5"
	bufTeal   = "#5fdcff"
	errorRed  = "#cc0000"
	errorPink = "#ff6666"
)

var (
	colorForeground = compat.AdaptiveColor{
		Light: lipgloss.Color(bufBlue),
		Dark:  lipgloss.Color(bufTeal),
	}
	colorBackground = compat.AdaptiveColor{
		Light: lipgloss.Color(bufTeal),
		Dark:  lipgloss.Color(bufBlue),
	}
	colorError = compat.AdaptiveColor{
		Light: lipgloss.Color(errorRed),
		Dark:  lipgloss.Color(errorPink),
	}
	codeStyleLight = chromastyles.Get("modus-operandi")
	codeStyleDark  = chromastyles.Get("modus-vivendi")
)

// renderHyperlink renders text as a terminal hyperlink to url.
func renderHyperlink(text, url string) string {
	return lipgloss.NewStyle().Hyperlink(url).Render(text)
}

// breadcrumb renders a › -separated sequence of hyperlinked segments.
// Each pair of (text, url) arguments produces one linked segment.
func breadcrumb(pairs ...string) string {
	if len(pairs)%2 != 0 {
		panic("breadcrumb requires an even number of arguments (text, url pairs)")
	}
	parts := make([]string, len(pairs)/2)
	for i := range parts {
		parts[i] = renderHyperlink(pairs[i*2], pairs[i*2+1])
	}
	return strings.Join(parts, " › ")
}

// helpStyles returns well-contrasted help bar styles for the given background.
// The default bubbles styles have near-invisible colors in both light and dark
// modes, so we override them using our brand colors for key names and readable
// grays for descriptions and separators.
func helpStyles(isDark bool) help.Styles {
	lightDark := lipgloss.LightDark(isDark)
	keyStyle := lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color(bufBlue), lipgloss.Color(bufTeal)))
	descStyle := lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#555555"), lipgloss.Color("#aaaaaa")))
	sepStyle := lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#aaaaaa"), lipgloss.Color("#555555")))
	return help.Styles{
		ShortKey:       keyStyle,
		ShortDesc:      descStyle,
		ShortSeparator: sepStyle,
		Ellipsis:       sepStyle,
		FullKey:        keyStyle,
		FullDesc:       descStyle,
		FullSeparator:  sepStyle,
	}
}
