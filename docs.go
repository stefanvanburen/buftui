package main

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// docsPackage is a list.Item representing all top-level proto entities in a
// single package. The list on the left shows one entry per package; selecting
// it renders the full entity docs in the viewport on the right.
type docsPackage struct {
	name       string
	syntax     string
	edition    string
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

// editionString returns the edition label as it appears in proto source
// (e.g. "2023" for EDITION_2023).
func editionString(e descriptorpb.Edition) string {
	return strings.TrimPrefix(e.String(), "EDITION_")
}

// derivedJSONName computes the default JSON name for a proto field name
// (snake_case -> camelCase), matching the protobuf compiler's algorithm.
// Compilers always populate FieldDescriptorProto.json_name, whether or not
// the .proto source contained an explicit override, so comparing against
// this derivation is the only reliable way to detect a genuine override.
func derivedJSONName(name string) string {
	var b strings.Builder
	capNext := false
	for _, r := range name {
		switch {
		case r == '_':
			capNext = true
		case capNext:
			b.WriteRune(unicode.ToUpper(r))
			capNext = false
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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
			p := &docsPackage{name: pkg}
			if fd.Syntax() == protoreflect.Editions {
				p.edition = editionString(protodesc.ToFileDescriptorProto(fd).GetEdition())
			} else {
				p.syntax = fd.Syntax().String()
			}
			byPkg[pkg] = p
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
			for line := range strings.SplitSeq(c, "\n") {
				b.WriteString(commentStyle.Render(line) + "\n")
			}
		}
	}

	annotate := func(d protoreflect.Descriptor) string {
		var s string
		if isDeprecated(d) {
			s += "  " + deprecatedStyle.Render("[deprecated]")
		}
		if enum, ok := d.(protoreflect.EnumDescriptor); ok && enum.IsClosed() {
			s += "  " + dimStyle.Render("[closed]")
		}
		if custom := customOptionsAnnotation(d.Options()); custom != "" {
			s += "  " + dimStyle.Render(custom)
		}
		return s
	}

	switch {
	case p.edition != "":
		b.WriteString(dimStyle.Render(fmt.Sprintf("edition = %q;", p.edition)) + "\n")
	case p.syntax != "":
		b.WriteString(dimStyle.Render(fmt.Sprintf("syntax = %q;", p.syntax)) + "\n")
	}

	for _, svc := range p.services {
		rule(string(svc.Name()) + annotate(svc))
		writeComment(svc)
		b.WriteString("\n")
		for i := range svc.Methods().Len() {
			m := svc.Methods().Get(i)
			b.WriteString(renderMethod(m, typeStyle, dimStyle, commentStyle))
			b.WriteString("\n")
		}
	}

	for _, msg := range p.messages {
		rule(string(msg.Name()) + annotate(msg))
		writeComment(msg)
		b.WriteString("\n")
		renderMessageFields(&b, msg, typeStyle, dimStyle, commentStyle)
		b.WriteString("\n")
		// Nested enum types, shown as subsections with dotted path.
		renderNestedEnums(&b, msg, string(msg.Name()), dimStyle, commentStyle, nameStyle, ruleStyle, annotate, writeComment)
		// Nested extend blocks, declared directly inside this message.
		renderNestedExtensions(&b, msg, typeStyle, dimStyle, commentStyle)
		// Nested message types, shown as subsections with dotted path.
		renderNestedMessages(&b, msg, string(msg.Name()), typeStyle, dimStyle, commentStyle, nameStyle, ruleStyle, annotate, writeComment)
	}

	for _, enum := range p.enums {
		rule(string(enum.Name()) + annotate(enum))
		writeComment(enum)
		b.WriteString("\n")
		for i := range enum.Values().Len() {
			v := enum.Values().Get(i)
			b.WriteString(renderEnumValue(v, enumValueAliasOf(enum, v), dimStyle, commentStyle))
		}
		renderEnumReserved(&b, enum, dimStyle)
		b.WriteString("\n")
	}

	for _, ext := range p.extensions {
		rule(string(ext.Name()) + annotate(ext))
		writeComment(ext)
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("extend %s {", ext.ContainingMessage().FullName())) + "\n")
		b.WriteString("  " + renderField(ext, typeStyle, dimStyle, commentStyle))
		b.WriteString(dimStyle.Render("}") + "\n\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderMessageFields renders a message's own fields, oneof blocks, reserved
// ranges/names, and extension ranges — everything about the message except
// its nested types.
func renderMessageFields(b *strings.Builder, msg protoreflect.MessageDescriptor, typeStyle, dimStyle, commentStyle lipgloss.Style) {
	// Non-oneof fields first. proto3 `optional` fields compile to a hidden
	// "synthetic" oneof containing just that field -- treat those as plain
	// fields rather than surfacing the synthetic oneof as a visible block.
	for i := range msg.Fields().Len() {
		f := msg.Fields().Get(i)
		if oneof := f.ContainingOneof(); oneof == nil || oneof.IsSynthetic() {
			b.WriteString(renderField(f, typeStyle, dimStyle, commentStyle))
		}
	}
	// Oneof blocks.
	for i := range msg.Oneofs().Len() {
		oneof := msg.Oneofs().Get(i)
		if oneof.IsSynthetic() {
			continue
		}
		if c := leadingComment(oneof); c != "" {
			for l := range strings.SplitSeq(c, "\n") {
				b.WriteString(commentStyle.Render(l) + "\n")
			}
		}
		header := fmt.Sprintf("oneof %s {", oneof.Name())
		if custom := customOptionsAnnotation(oneof.Options()); custom != "" {
			header += "  " + custom
		}
		b.WriteString(dimStyle.Render(header) + "\n")
		for j := range oneof.Fields().Len() {
			b.WriteString("  " + renderField(oneof.Fields().Get(j), typeStyle, dimStyle, commentStyle))
		}
		b.WriteString(dimStyle.Render("}") + "\n")
	}
	renderRanges(b, "reserved", msg.ReservedRanges(), dimStyle)
	for i := range msg.ReservedNames().Len() {
		b.WriteString(dimStyle.Render(fmt.Sprintf("reserved %q;", msg.ReservedNames().Get(i))) + "\n")
	}
	// Extension ranges — field numbers reserved for third-party extensions.
	// Rendered separately from renderRanges since each range can carry its
	// own custom options (e.g. a declaration of who owns the range).
	for i := range msg.ExtensionRanges().Len() {
		r := msg.ExtensionRanges().Get(i)
		lo, hi := int(r[0]), int(r[1])-1
		var text string
		switch {
		case protowire.Number(hi) == protowire.MaxValidNumber:
			text = fmt.Sprintf("extensions %d to max;", lo)
		case lo == hi:
			text = fmt.Sprintf("extensions %d;", lo)
		default:
			text = fmt.Sprintf("extensions %d to %d;", lo, hi)
		}
		if custom := customOptionsAnnotation(msg.ExtensionRangeOptions(i)); custom != "" {
			text += "  " + custom
		}
		b.WriteString(dimStyle.Render(text) + "\n")
	}
}

// renderRanges renders field-number ranges, used for both "reserved" and
// "extensions" declarations, collapsing to "N;", "N to M;", or "N to max;".
func renderRanges(b *strings.Builder, keyword string, ranges protoreflect.FieldRanges, dimStyle lipgloss.Style) {
	for i := range ranges.Len() {
		r := ranges.Get(i)
		lo, hi := int(r[0]), int(r[1])-1
		switch {
		case protowire.Number(hi) == protowire.MaxValidNumber:
			b.WriteString(dimStyle.Render(fmt.Sprintf("%s %d to max;", keyword, lo)) + "\n")
		case lo == hi:
			b.WriteString(dimStyle.Render(fmt.Sprintf("%s %d;", keyword, lo)) + "\n")
		default:
			b.WriteString(dimStyle.Render(fmt.Sprintf("%s %d to %d;", keyword, lo, hi)) + "\n")
		}
	}
}

// renderEnumReserved renders an enum's reserved ranges and names.
func renderEnumReserved(b *strings.Builder, enum protoreflect.EnumDescriptor, dimStyle lipgloss.Style) {
	renderEnumRanges(b, enum.ReservedRanges(), dimStyle)
	for i := range enum.ReservedNames().Len() {
		b.WriteString(dimStyle.Render(fmt.Sprintf("reserved %q;", enum.ReservedNames().Get(i))) + "\n")
	}
}

// renderEnumRanges renders enum reserved-number ranges as "N;" or "N to M;".
// Unlike protoreflect.FieldRanges (half-open), protoreflect.EnumRanges are
// fully inclusive, so this can't share renderRanges' off-by-one handling.
func renderEnumRanges(b *strings.Builder, ranges protoreflect.EnumRanges, dimStyle lipgloss.Style) {
	for i := range ranges.Len() {
		r := ranges.Get(i)
		lo, hi := int32(r[0]), int32(r[1])
		if lo == hi {
			b.WriteString(dimStyle.Render(fmt.Sprintf("reserved %d;", lo)) + "\n")
		} else {
			b.WriteString(dimStyle.Render(fmt.Sprintf("reserved %d to %d;", lo, hi)) + "\n")
		}
	}
}

// renderNestedEnums renders enum types declared directly inside msg as
// subsections with a dotted path prefix (e.g. "Outer.Status").
func renderNestedEnums(
	b *strings.Builder,
	msg protoreflect.MessageDescriptor,
	path string,
	dimStyle, commentStyle, nameStyle, ruleStyle lipgloss.Style,
	annotateFn func(protoreflect.Descriptor) string,
	writeCommentFn func(protoreflect.Descriptor),
) {
	for i := range msg.Enums().Len() {
		enum := msg.Enums().Get(i)
		subPath := path + "." + string(enum.Name())
		b.WriteString("\n" + nameStyle.Render(subPath+annotateFn(enum)) + "\n")
		b.WriteString(ruleStyle.Render(strings.Repeat("─", len(subPath))) + "\n")
		writeCommentFn(enum)
		b.WriteString("\n")
		for j := range enum.Values().Len() {
			v := enum.Values().Get(j)
			b.WriteString(renderEnumValue(v, enumValueAliasOf(enum, v), dimStyle, commentStyle))
		}
		renderEnumReserved(b, enum, dimStyle)
		b.WriteString("\n")
	}
}

// renderNestedExtensions renders extend blocks declared directly inside msg.
func renderNestedExtensions(b *strings.Builder, msg protoreflect.MessageDescriptor, typeStyle, dimStyle, commentStyle lipgloss.Style) {
	for i := range msg.Extensions().Len() {
		ext := msg.Extensions().Get(i)
		b.WriteString(dimStyle.Render(fmt.Sprintf("extend %s {", ext.ContainingMessage().FullName())) + "\n")
		b.WriteString("  " + renderField(ext, typeStyle, dimStyle, commentStyle))
		b.WriteString(dimStyle.Render("}") + "\n\n")
	}
}

// renderNestedMessages recursively renders nested message types as subsections
// with a dotted path prefix (e.g. "Outer.Inner").
func renderNestedMessages(
	b *strings.Builder,
	msg protoreflect.MessageDescriptor,
	path string,
	typeStyle, dimStyle, commentStyle, nameStyle, ruleStyle lipgloss.Style,
	annotateFn func(protoreflect.Descriptor) string,
	writeCommentFn func(protoreflect.Descriptor),
) {
	for i := range msg.Messages().Len() {
		nested := msg.Messages().Get(i)
		if nested.IsMapEntry() {
			continue // synthetic map entry — not a real nested type
		}
		subPath := path + "." + string(nested.Name())
		b.WriteString("\n" + nameStyle.Render(subPath+annotateFn(nested)) + "\n")
		b.WriteString(ruleStyle.Render(strings.Repeat("─", len(subPath))) + "\n")
		writeCommentFn(nested)
		b.WriteString("\n")
		renderMessageFields(b, nested, typeStyle, dimStyle, commentStyle)
		b.WriteString("\n")
		renderNestedEnums(b, nested, subPath, dimStyle, commentStyle, nameStyle, ruleStyle, annotateFn, writeCommentFn)
		renderNestedExtensions(b, nested, typeStyle, dimStyle, commentStyle)
		renderNestedMessages(b, nested, subPath, typeStyle, dimStyle, commentStyle, nameStyle, ruleStyle, annotateFn, writeCommentFn)
	}
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
	if custom := customOptionsAnnotation(m.Options()); custom != "" {
		line += "  " + dimStyle.Render(custom)
	}
	b.WriteString(line + "\n")
	if c := leadingComment(m); c != "" {
		for l := range strings.SplitSeq(c, "\n") {
			b.WriteString("  " + commentStyle.Render(l) + "\n")
		}
	}
	return b.String()
}

func renderField(f protoreflect.FieldDescriptor, typeStyle, dimStyle, commentStyle lipgloss.Style) string {
	var b strings.Builder
	typeName := fieldTypeName(f)
	if f.Kind() == protoreflect.GroupKind {
		typeName = "group " + typeName
	}
	switch {
	case f.IsList():
		typeName = "repeated " + typeName
	case f.Cardinality() == protoreflect.Required:
		typeName = "required " + typeName
	case f.HasOptionalKeyword():
		// Covers both a proto2 field explicitly declared "optional" and a
		// proto3/editions field with explicit presence (which compiles to a
		// hidden synthetic oneof that's never rendered as its own block, so
		// the presence tracking is surfaced here instead).
		typeName = "optional " + typeName
	}
	line := fmt.Sprintf("%s %s = %d",
		typeStyle.Render(typeName),
		string(f.Name()),
		f.Number(),
	)
	if f.HasDefault() {
		line += "  " + dimStyle.Render(fmt.Sprintf("[default = %s]", formatSingularOptionValue(f, f.Default())))
	}
	wantJSONName := derivedJSONName(string(f.Name()))
	if f.IsExtension() {
		// Extension fields always get an automatic "[pkg.field]" JSON name
		// per the protobuf spec -- that's not an author-written override.
		wantJSONName = fmt.Sprintf("[%s]", f.FullName())
	}
	if f.JSONName() != wantJSONName {
		line += "  " + dimStyle.Render(fmt.Sprintf("[json_name = %q]", f.JSONName()))
	}
	if hasExplicitOption(f.Options(), "packed") {
		line += "  " + dimStyle.Render(fmt.Sprintf("[packed = %v]", f.IsPacked()))
	}
	if opts, ok := f.Options().(*descriptorpb.FieldOptions); ok && opts != nil && opts.GetDeprecated() {
		line += "  " + dimStyle.Render("[deprecated]")
	}
	if opts, ok := f.Options().(*descriptorpb.FieldOptions); ok && opts != nil && opts.GetDebugRedact() {
		line += "  " + dimStyle.Render("[debug_redact]")
	}
	if custom := customOptionsAnnotation(f.Options()); custom != "" {
		line += "  " + dimStyle.Render(custom)
	}
	b.WriteString(line + "\n")
	if c := leadingComment(f); c != "" {
		for l := range strings.SplitSeq(c, "\n") {
			b.WriteString("  " + commentStyle.Render(l) + "\n")
		}
	}
	return b.String()
}

// enumValueAliasOf returns the name of the canonical enum value that v is an
// alias of (shares its number with an earlier-declared value in enum), or ""
// if v is itself canonical.
func enumValueAliasOf(enum protoreflect.EnumDescriptor, v protoreflect.EnumValueDescriptor) string {
	canonical := enum.Values().ByNumber(v.Number())
	if canonical == nil || canonical.Name() == v.Name() {
		return ""
	}
	return string(canonical.Name())
}

func renderEnumValue(v protoreflect.EnumValueDescriptor, aliasOf string, dimStyle, commentStyle lipgloss.Style) string {
	var b strings.Builder
	line := fmt.Sprintf("%s = %d", string(v.Name()), v.Number())
	if aliasOf != "" {
		line += "  " + dimStyle.Render(fmt.Sprintf("[alias of %s]", aliasOf))
	}
	if opts, ok := v.Options().(*descriptorpb.EnumValueOptions); ok && opts != nil && opts.GetDeprecated() {
		line += "  " + dimStyle.Render("[deprecated]")
	}
	if custom := customOptionsAnnotation(v.Options()); custom != "" {
		line += "  " + dimStyle.Render(custom)
	}
	b.WriteString(line + "\n")
	if c := leadingComment(v); c != "" {
		for l := range strings.SplitSeq(c, "\n") {
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

// hasExplicitOption reports whether the named field was explicitly set on
// opts, as opposed to merely having a well-defined effective/default value.
// Some options (like FieldOptions.packed) have syntax-dependent implicit
// defaults, so an accessor's return value alone can't tell us whether the
// .proto source actually wrote it out.
func hasExplicitOption(opts protoreflect.ProtoMessage, name protoreflect.Name) bool {
	if opts == nil {
		return false
	}
	m := opts.ProtoReflect()
	if !m.IsValid() {
		return false
	}
	fd := m.Descriptor().Fields().ByName(name)
	if fd == nil {
		return false
	}
	return m.Has(fd)
}

// customOptionsAnnotation returns a "[(pkg.ext) = value, ...]" annotation
// listing any custom (extension) options set on opts, or "" if there are
// none. Standard, non-extension fields (e.g. deprecated) are rendered
// elsewhere and are intentionally excluded here to avoid duplication.
func customOptionsAnnotation(opts protoreflect.ProtoMessage) string {
	if opts == nil {
		return ""
	}
	m := opts.ProtoReflect()
	if !m.IsValid() {
		return ""
	}
	var parts []string
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if !fd.IsExtension() {
			return true
		}
		parts = append(parts, fmt.Sprintf("(%s) = %s", fd.FullName(), formatOptionValue(fd, v)))
		return true
	})
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// formatOptionValue formats a single option field's value for display,
// expanding repeated values into a bracketed list.
func formatOptionValue(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	if fd.IsList() {
		list := v.List()
		parts := make([]string, list.Len())
		for i := range list.Len() {
			parts[i] = formatSingularOptionValue(fd, list.Get(i))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return formatSingularOptionValue(fd, v)
}

// formatSingularOptionValue formats one scalar/message/enum option value.
// Message-kind values are formatted with prototext, which works for
// dynamic (unrecognized-at-compile-time) messages just as well as
// generated ones.
func formatSingularOptionValue(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		text, err := prototext.MarshalOptions{Multiline: false}.Marshal(v.Message().Interface())
		if err != nil {
			return "{ ... }"
		}
		return "{ " + strings.TrimSpace(string(text)) + " }"
	case protoreflect.EnumKind:
		if ev := fd.Enum().Values().ByNumber(v.Enum()); ev != nil {
			return string(ev.Name())
		}
		return fmt.Sprintf("%d", v.Enum())
	case protoreflect.StringKind:
		return fmt.Sprintf("%q", v.String())
	case protoreflect.BytesKind:
		return fmt.Sprintf("%q", v.Bytes())
	default:
		return fmt.Sprintf("%v", v.Interface())
	}
}

// fieldTypeName returns a human-readable type name for a field, using
// fully-qualified names for types from other packages.
func fieldTypeName(f protoreflect.FieldDescriptor) string {
	if f.IsMap() {
		key := f.MapKey().Kind().String()
		val := fieldScalarOrRefName(f.MapValue(), f.ParentFile().Package())
		return fmt.Sprintf("map<%s, %s>", key, val)
	}
	return fieldScalarOrRefName(f, f.ParentFile().Package())
}

// fieldScalarOrRefName returns the type name for a field, qualifying
// message/enum types from other packages with their full package path.
func fieldScalarOrRefName(f protoreflect.FieldDescriptor, pkg protoreflect.FullName) string {
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		msg := f.Message()
		if msg.ParentFile().Package() != pkg {
			return string(msg.FullName())
		}
		return string(msg.Name())
	case protoreflect.EnumKind:
		en := f.Enum()
		if en.ParentFile().Package() != pkg {
			return string(en.FullName())
		}
		return string(en.Name())
	default:
		return f.Kind().String()
	}
}
