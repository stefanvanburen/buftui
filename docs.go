package main

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// docsPackage is a list.Item representing all top-level proto entities in a
// single package. The list on the left shows one entry per package; selecting
// it renders the full entity docs in the viewport on the right.
type docsPackage struct {
	name       string
	services   []protoreflect.ServiceDescriptor
	messages   []protoreflect.MessageDescriptor
	enums      []protoreflect.EnumDescriptor
	extensions []protoreflect.ExtensionDescriptor
}

func (p *docsPackage) FilterValue() string { return p.name }
func (p *docsPackage) Title() string       { return p.name }
func (p *docsPackage) Description() string {
	var parts []string
	if n := len(p.services); n > 0 {
		parts = append(parts, fmt.Sprintf("%d service%s", n, plural(n)))
	}
	if n := len(p.messages); n > 0 {
		parts = append(parts, fmt.Sprintf("%d message%s", n, plural(n)))
	}
	if n := len(p.enums); n > 0 {
		parts = append(parts, fmt.Sprintf("%d enum%s", n, plural(n)))
	}
	if n := len(p.extensions); n > 0 {
		parts = append(parts, fmt.Sprintf("%d extension%s", n, plural(n)))
	}
	return strings.Join(parts, " · ")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// packagesFromDocs groups top-level entities from the own-module files by
// package, sorts packages alphabetically, and sorts entities within each
// package by FQN.
func packagesFromDocs(files *protoregistry.Files, ownPaths map[string]bool) []list.Item {
	byPkg := make(map[string]*docsPackage)

	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if !ownPaths[fd.Path()] {
			return true
		}
		pkg := string(fd.Package())
		if _, ok := byPkg[pkg]; !ok {
			byPkg[pkg] = &docsPackage{name: pkg}
		}
		p := byPkg[pkg]
		for i := range fd.Services().Len() {
			p.services = append(p.services, fd.Services().Get(i))
		}
		for i := range fd.Messages().Len() {
			p.messages = append(p.messages, fd.Messages().Get(i))
		}
		for i := range fd.Enums().Len() {
			p.enums = append(p.enums, fd.Enums().Get(i))
		}
		for i := range fd.Extensions().Len() {
			p.extensions = append(p.extensions, fd.Extensions().Get(i))
		}
		return true
	})

	byFQN := func(a, b protoreflect.Descriptor) int {
		return strings.Compare(string(a.FullName()), string(b.FullName()))
	}
	byFQNSvc := func(a, b protoreflect.ServiceDescriptor) int { return byFQN(a, b) }
	byFQNMsg := func(a, b protoreflect.MessageDescriptor) int { return byFQN(a, b) }
	byFQNEnum := func(a, b protoreflect.EnumDescriptor) int { return byFQN(a, b) }
	byFQNExt := func(a, b protoreflect.ExtensionDescriptor) int { return byFQN(a, b) }

	pkgs := make([]list.Item, 0, len(byPkg))
	for _, p := range byPkg {
		slices.SortStableFunc(p.services, byFQNSvc)
		slices.SortStableFunc(p.messages, byFQNMsg)
		slices.SortStableFunc(p.enums, byFQNEnum)
		slices.SortStableFunc(p.extensions, byFQNExt)
		pkgs = append(pkgs, p)
	}
	slices.SortStableFunc(pkgs, func(a, b list.Item) int {
		return strings.Compare(a.(*docsPackage).name, b.(*docsPackage).name)
	})
	return pkgs
}

// renderPackage renders a full documentation page for a package — all its
// services, messages, enums, and extensions — suitable for the viewport.
func renderPackage(p *docsPackage, isDark bool) string {
	lightDark := lipgloss.LightDark(isDark)
	nameStyle := lipgloss.NewStyle().Foreground(colorForeground).Bold(true)
	ruleStyle := lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#cccccc"), lipgloss.Color("#444444")))
	dimStyle := lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#555555"), lipgloss.Color("#aaaaaa")))
	typeStyle := lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#0060aa"), lipgloss.Color("#88ccff")))
	commentStyle := lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#448844"), lipgloss.Color("#88bb88"))).Italic(true)
	deprecatedStyle := lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#aa4400"), lipgloss.Color("#ff8866")))

	var b strings.Builder

	rule := func(name string) {
		b.WriteString("\n" + nameStyle.Render(name) + "\n")
		b.WriteString(ruleStyle.Render(strings.Repeat("─", len(name))) + "\n")
	}

	writeComment := func(d protoreflect.Descriptor) {
		if c := leadingComment(d); c != "" {
			for _, line := range strings.Split(c, "\n") {
				b.WriteString(commentStyle.Render(line) + "\n")
			}
		}
	}

	deprecated := func(d protoreflect.Descriptor) string {
		if isDeprecated(d) {
			return "  " + deprecatedStyle.Render("[deprecated]")
		}
		return ""
	}

	for _, svc := range p.services {
		rule(string(svc.Name()) + deprecated(svc))
		writeComment(svc)
		b.WriteString("\n")
		for i := range svc.Methods().Len() {
			m := svc.Methods().Get(i)
			b.WriteString(renderMethod(m, typeStyle, dimStyle, commentStyle))
			b.WriteString("\n")
		}
	}

	for _, msg := range p.messages {
		rule(string(msg.Name()) + deprecated(msg))
		writeComment(msg)
		b.WriteString("\n")
		for i := range msg.Fields().Len() {
			b.WriteString(renderField(msg.Fields().Get(i), typeStyle, dimStyle, commentStyle))
		}
		b.WriteString("\n")
	}

	for _, enum := range p.enums {
		rule(string(enum.Name()) + deprecated(enum))
		writeComment(enum)
		b.WriteString("\n")
		for i := range enum.Values().Len() {
			b.WriteString(renderEnumValue(enum.Values().Get(i), dimStyle, commentStyle))
		}
		b.WriteString("\n")
	}

	for _, ext := range p.extensions {
		rule(string(ext.Name()) + deprecated(ext))
		writeComment(ext)
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("extend %s {", ext.ContainingMessage().FullName())) + "\n")
		typeName := fieldTypeName(ext)
		b.WriteString(fmt.Sprintf("  %s %s = %d;\n",
			typeStyle.Render(typeName),
			string(ext.Name()),
			ext.Number(),
		))
		b.WriteString(dimStyle.Render("}") + "\n\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

func renderMethod(m protoreflect.MethodDescriptor, typeStyle, dimStyle, commentStyle lipgloss.Style) string {
	var b strings.Builder

	input := string(m.Input().Name())
	output := string(m.Output().Name())
	if m.IsStreamingClient() {
		input = "stream " + input
	}
	if m.IsStreamingServer() {
		output = "stream " + output
	}
	line := fmt.Sprintf("rpc %s(%s) returns (%s)",
		string(m.Name()),
		typeStyle.Render(input),
		typeStyle.Render(output),
	)
	var annotations []string
	if opts, ok := m.Options().(*descriptorpb.MethodOptions); ok && opts != nil {
		switch opts.GetIdempotencyLevel() {
		case descriptorpb.MethodOptions_NO_SIDE_EFFECTS:
			annotations = append(annotations, "no side effects")
		case descriptorpb.MethodOptions_IDEMPOTENT:
			annotations = append(annotations, "idempotent")
		}
		if opts.GetDeprecated() {
			annotations = append(annotations, "deprecated")
		}
	}
	if len(annotations) > 0 {
		line += "  " + dimStyle.Render("["+strings.Join(annotations, ", ")+"]")
	}
	b.WriteString(line + "\n")
	if c := leadingComment(m); c != "" {
		for _, l := range strings.Split(c, "\n") {
			b.WriteString("  " + commentStyle.Render(l) + "\n")
		}
	}
	return b.String()
}

func renderField(f protoreflect.FieldDescriptor, typeStyle, dimStyle, commentStyle lipgloss.Style) string {
	var b strings.Builder
	typeName := fieldTypeName(f)
	if f.IsList() {
		typeName = "repeated " + typeName
	} else if f.IsMap() {
		typeName = "map"
	}
	line := fmt.Sprintf("%s %s = %d",
		typeStyle.Render(typeName),
		string(f.Name()),
		f.Number(),
	)
	if opts, ok := f.Options().(*descriptorpb.FieldOptions); ok && opts != nil && opts.GetDeprecated() {
		line += "  " + dimStyle.Render("[deprecated]")
	}
	b.WriteString(line + "\n")
	if c := leadingComment(f); c != "" {
		for _, l := range strings.Split(c, "\n") {
			b.WriteString("  " + commentStyle.Render(l) + "\n")
		}
	}
	return b.String()
}

func renderEnumValue(v protoreflect.EnumValueDescriptor, dimStyle, commentStyle lipgloss.Style) string {
	var b strings.Builder
	line := fmt.Sprintf("%s = %d", string(v.Name()), v.Number())
	if opts, ok := v.Options().(*descriptorpb.EnumValueOptions); ok && opts != nil && opts.GetDeprecated() {
		line += "  " + dimStyle.Render("[deprecated]")
	}
	b.WriteString(line + "\n")
	if c := leadingComment(v); c != "" {
		for _, l := range strings.Split(c, "\n") {
			b.WriteString("  " + commentStyle.Render(l) + "\n")
		}
	}
	return b.String()
}

// cleanComment strips the per-line leading space that proto source info
// stores (from `// comment` → ` comment`) and trims surrounding newlines.
func cleanComment(raw string) string {
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		cleaned = append(cleaned, strings.TrimPrefix(line, " "))
	}
	return strings.Trim(strings.Join(cleaned, "\n"), "\n")
}

// leadingComment returns the cleaned leading comment for a descriptor, or "".
func leadingComment(d protoreflect.Descriptor) string {
	return cleanComment(d.ParentFile().SourceLocations().ByDescriptor(d).LeadingComments)
}

// isDeprecated reports whether the descriptor has the deprecated option set.
func isDeprecated(d protoreflect.Descriptor) bool {
	switch opts := d.Options().(type) {
	case *descriptorpb.ServiceOptions:
		return opts.GetDeprecated()
	case *descriptorpb.MessageOptions:
		return opts.GetDeprecated()
	case *descriptorpb.EnumOptions:
		return opts.GetDeprecated()
	case *descriptorpb.FieldOptions:
		return opts.GetDeprecated()
	}
	return false
}

// fieldTypeName returns a human-readable type name for a field.
func fieldTypeName(f protoreflect.FieldDescriptor) string {
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return string(f.Message().Name())
	case protoreflect.EnumKind:
		return string(f.Enum().Name())
	default:
		return f.Kind().String()
	}
}
