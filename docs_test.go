package main

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"go.akshayshah.org/attest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
)

// buildTestRegistry creates a *protoregistry.Files from a FileDescriptorProto.
func buildTestRegistry(t *testing.T, fdp *descriptorpb.FileDescriptorProto) *protoregistry.Files {
	t.Helper()
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}})
	attest.Ok(t, err, attest.Fatal())
	return files
}

// buildExtensionType resolves an extension descriptor extending extendee
// (e.g. ".google.protobuf.MethodOptions") at the given field number, purely
// from descriptor protos, and returns a dynamicpb-derived ExtensionType for
// it. This mirrors how a real third-party custom option (google.api.http,
// buf.validate.field, ...) is known only by descriptor, with no generated
// Go package for it linked into the binary.
func buildExtensionType(t *testing.T, extendee, name string, number int32, typ descriptorpb.FieldDescriptorProto_Type, typeName string) protoreflect.ExtensionType {
	t.Helper()
	descProtoFDP := protodesc.ToFileDescriptorProto((&descriptorpb.FileOptions{}).ProtoReflect().Descriptor().ParentFile())
	extFDP := &descriptorpb.FileDescriptorProto{
		Name:       new("testext.proto"),
		Syntax:     new("proto3"),
		Package:    new("testext"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		Extension: []*descriptorpb.FieldDescriptorProto{{
			Name:     new(name),
			Number:   new(number),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     typ.Enum(),
			TypeName: nonEmptyPtr(typeName),
			Extendee: new(extendee),
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Detail"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("name"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
		}},
	}
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{descProtoFDP, extFDP}})
	attest.Ok(t, err, attest.Fatal())
	d, err := files.FindDescriptorByName(protoreflect.FullName("testext." + name))
	attest.Ok(t, err, attest.Fatal())
	return dynamicpb.NewExtensionType(d.(protoreflect.ExtensionDescriptor))
}

// nonEmptyPtr returns nil for an empty string, or a pointer to s otherwise --
// FieldDescriptorProto.TypeName must be unset for non-message/enum fields.
func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func TestCleanComment(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "empty",
			raw:  "",
			want: "",
		},
		{
			name: "single line with leading space",
			raw:  " Hello.\n",
			want: "Hello.",
		},
		{
			name: "multi-line strips per-line leading space",
			raw:  " First line.\n Second line.\n Third line.\n",
			want: "First line.\nSecond line.\nThird line.",
		},
		{
			name: "no leading spaces left untouched",
			raw:  "Already clean.\nStill clean.\n",
			want: "Already clean.\nStill clean.",
		},
		{
			name: "only one leading space stripped per line",
			raw:  "  double indent.\n single indent.\n",
			want: " double indent.\nsingle indent.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cleanComment(tc.raw)
			attest.Equal(t, got, tc.want)
		})
	}
}

func TestPackagesFromDocs_Ordering(t *testing.T) {
	t.Parallel()

	// A proto2 file with 2 services, 2 messages (one with extension range),
	// 1 enum, and 1 extension — all in the same package, declared in reverse
	// alphabetical order to verify sorting is applied within each kind.
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("test.proto"),
		Syntax:  new("proto2"),
		Package: new("test"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: new("Zebra"),
				ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
					{Start: new(int32(1000)), End: new(int32(2000))},
				},
			},
			{Name: new("Apple")},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name: new("Status"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: new("STATUS_UNSPECIFIED"), Number: new(int32(0))},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: new("ZebraService")},
			{Name: new("AppleService")},
		},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     new("my_ext"),
				Number:   new(int32(1000)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Extendee: new(".test.Zebra"),
			},
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"test.proto": true})

	attest.Equal(t, len(items), 1, attest.Sprintf("expected 1 package, got %d", len(items)))

	pkg := items[0].(*docsPackage)
	attest.Equal(t, pkg.name, "test")

	// Services sorted by FQN.
	attest.Equal(t, len(pkg.services), 2)
	attest.Equal(t, string(pkg.services[0].Name()), "AppleService")
	attest.Equal(t, string(pkg.services[1].Name()), "ZebraService")

	// Messages sorted by FQN.
	attest.Equal(t, len(pkg.messages), 2)
	attest.Equal(t, string(pkg.messages[0].Name()), "Apple")
	attest.Equal(t, string(pkg.messages[1].Name()), "Zebra")

	// Enums.
	attest.Equal(t, len(pkg.enums), 1)
	attest.Equal(t, string(pkg.enums[0].Name()), "Status")

	// Extensions.
	attest.Equal(t, len(pkg.extensions), 1)
	attest.Equal(t, string(pkg.extensions[0].Name()), "my_ext")
}

func TestPackagesFromDocs_MultiplePackages(t *testing.T) {
	t.Parallel()

	// Two files in the same module, different packages — should produce two
	// package entries sorted alphabetically.
	fdp1 := &descriptorpb.FileDescriptorProto{
		Name:    new("z.proto"),
		Syntax:  new("proto3"),
		Package: new("z.pkg"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("ZMsg")},
		},
	}
	fdp2 := &descriptorpb.FileDescriptorProto{
		Name:    new("a.proto"),
		Syntax:  new("proto3"),
		Package: new("a.pkg"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("AMsg")},
		},
	}

	reg, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{fdp1, fdp2},
	})
	attest.Ok(t, err, attest.Fatal())

	ownPaths := map[string]bool{"z.proto": true, "a.proto": true}
	items := packagesFromDocs(reg, ownPaths)

	attest.Equal(t, len(items), 2)
	attest.Equal(t, items[0].(*docsPackage).name, "a.pkg")
	attest.Equal(t, items[1].(*docsPackage).name, "z.pkg")
}

func TestPackagesFromDocs_OwnPathFilter(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("dep.proto"),
		Syntax:  new("proto3"),
		Package: new("dep"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("DepMessage")},
		},
	}

	files := buildTestRegistry(t, fdp)
	// dep.proto is NOT in ownPaths — should return nothing.
	items := packagesFromDocs(files, map[string]bool{"other.proto": true})
	attest.Equal(t, len(items), 0)
}

func TestPackagesFromDocs_SortsByFQN(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("fqn.proto"),
		Syntax:  new("proto3"),
		Package: new("z"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("Apple")},  // FQN: z.Apple
			{Name: new("Mango")},  // FQN: z.Mango
			{Name: new("Banana")}, // FQN: z.Banana
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"fqn.proto": true})

	attest.Equal(t, len(items), 1)
	pkg := items[0].(*docsPackage)
	names := make([]string, len(pkg.messages))
	for i, m := range pkg.messages {
		names[i] = string(m.FullName())
	}
	attest.Equal(t, names, []string{"z.Apple", "z.Banana", "z.Mango"})
}

func TestIsDeprecated(t *testing.T) {
	t.Parallel()

	deprecated := true
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("deprecated.proto"),
		Syntax:  new("proto3"),
		Package: new("dep"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:    new("OldMessage"),
				Options: &descriptorpb.MessageOptions{Deprecated: &deprecated},
			},
			{Name: new("NewMessage")},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name:    new("OldEnum"),
				Options: &descriptorpb.EnumOptions{Deprecated: &deprecated},
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: new("OLD_ENUM_UNSPECIFIED"), Number: new(int32(0))},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name:    new("OldService"),
				Options: &descriptorpb.ServiceOptions{Deprecated: &deprecated},
			},
		},
	}

	files := buildTestRegistry(t, fdp)
	var oldMsg, newMsg protoreflect.MessageDescriptor
	var oldEnum protoreflect.EnumDescriptor
	var oldSvc protoreflect.ServiceDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		oldMsg = fd.Messages().Get(0)
		newMsg = fd.Messages().Get(1)
		oldEnum = fd.Enums().Get(0)
		oldSvc = fd.Services().Get(0)
		return false
	})

	attest.True(t, isDeprecated(oldMsg))
	attest.False(t, isDeprecated(newMsg))
	attest.True(t, isDeprecated(oldEnum))
	attest.True(t, isDeprecated(oldSvc))
}

func TestPackageDescription(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("desc.proto"),
		Syntax:  new("proto3"),
		Package: new("desc"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("A")},
			{Name: new("B")},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name: new("E"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: new("E_UNSPECIFIED"), Number: new(int32(0))},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: new("Svc")},
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"desc.proto": true})
	attest.Equal(t, len(items), 1)

	desc := items[0].(*docsPackage).Description()
	attest.True(t, strings.Contains(desc, "1 service"), attest.Sprintf("description: %q", desc))
	attest.True(t, strings.Contains(desc, "2 messages"), attest.Sprintf("description: %q", desc))
	attest.True(t, strings.Contains(desc, "1 enum"), attest.Sprintf("description: %q", desc))
}

func TestRenderMethod_IdempotencyAndDeprecation(t *testing.T) {
	t.Parallel()

	deprecated := true
	noSideEffects := descriptorpb.MethodOptions_NO_SIDE_EFFECTS
	idempotent := descriptorpb.MethodOptions_IDEMPOTENT

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("svc.proto"),
		Syntax:  new("proto3"),
		Package: new("svc"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("Req")},
			{Name: new("Resp")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: new("Svc"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{
					Name:       new("Plain"),
					InputType:  new(".svc.Req"),
					OutputType: new(".svc.Resp"),
				},
				{
					Name:       new("ReadOnly"),
					InputType:  new(".svc.Req"),
					OutputType: new(".svc.Resp"),
					Options:    &descriptorpb.MethodOptions{IdempotencyLevel: &noSideEffects},
				},
				{
					Name:       new("Idempotent"),
					InputType:  new(".svc.Req"),
					OutputType: new(".svc.Resp"),
					Options:    &descriptorpb.MethodOptions{IdempotencyLevel: &idempotent},
				},
				{
					Name:       new("OldMethod"),
					InputType:  new(".svc.Req"),
					OutputType: new(".svc.Resp"),
					Options:    &descriptorpb.MethodOptions{Deprecated: &deprecated},
				},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	var svc protoreflect.ServiceDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		svc = fd.Services().Get(0)
		return false
	})

	typeStyle := lipgloss.NewStyle()
	dim := lipgloss.NewStyle()
	comment := lipgloss.NewStyle()

	plain := renderMethod(svc.Methods().Get(0), nil, typeStyle, dim, comment)
	readOnly := renderMethod(svc.Methods().Get(1), nil, typeStyle, dim, comment)
	idempotentOut := renderMethod(svc.Methods().Get(2), nil, typeStyle, dim, comment)
	oldMethod := renderMethod(svc.Methods().Get(3), nil, typeStyle, dim, comment)

	attest.False(t, strings.Contains(plain, "no side effects"),
		attest.Sprintf("plain: %q", plain))
	attest.False(t, strings.Contains(plain, "deprecated"),
		attest.Sprintf("plain: %q", plain))
	attest.True(t, strings.Contains(readOnly, "no side effects"),
		attest.Sprintf("readOnly: %q", readOnly))
	attest.True(t, strings.Contains(idempotentOut, "idempotent"),
		attest.Sprintf("idempotent: %q", idempotentOut))
	attest.True(t, strings.Contains(oldMethod, "deprecated"),
		attest.Sprintf("oldMethod: %q", oldMethod))
}

func TestFieldTypeName_Map(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("map.proto"),
		Syntax:  new("proto3"),
		Package: new("mypkg"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("Inner")},
			{
				Name: new("Outer"),
				Field: []*descriptorpb.FieldDescriptorProto{
					// map<string, Inner> — proto encodes as a synthetic map entry message
					{
						Name:     new("my_map"),
						Number:   new(int32(1)),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".mypkg.Outer.MyMapEntry"),
					},
				},
				NestedType: []*descriptorpb.DescriptorProto{
					{
						Name:    new("MyMapEntry"),
						Options: &descriptorpb.MessageOptions{MapEntry: new(true)},
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:   new("key"),
								Number: new(int32(1)),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
							{
								Name:     new("value"),
								Number:   new(int32(2)),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
								TypeName: new(".mypkg.Inner"),
							},
						},
					},
				},
			},
		},
	}

	files := buildTestRegistry(t, fdp)
	var outerMsg protoreflect.MessageDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		// Messages() returns top-level only; Outer is index 1 (after Inner).
		outerMsg = fd.Messages().Get(1)
		return false
	})

	mapField := outerMsg.Fields().Get(0)
	attest.True(t, mapField.IsMap(), attest.Sprintf("expected map field, got %v", mapField.Name()))
	typeName := fieldTypeName(mapField)
	attest.Equal(t, typeName, "map<string, Inner>")
}

func TestFieldTypeName_CrossPackage(t *testing.T) {
	t.Parallel()

	// OtherMsg is in package "other"; Request is in package "svc" and
	// references OtherMsg — the rendered type should be fully qualified.
	dep := &descriptorpb.FileDescriptorProto{
		Name:    new("other.proto"),
		Syntax:  new("proto3"),
		Package: new("other"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("OtherMsg")},
		},
	}
	main := &descriptorpb.FileDescriptorProto{
		Name:       new("svc.proto"),
		Syntax:     new("proto3"),
		Package:    new("svc"),
		Dependency: []string{"other.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: new("Request"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     new("other"),
						Number:   new(int32(1)),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".other.OtherMsg"),
					},
					{
						Name:     new("same_pkg"),
						Number:   new(int32(2)),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".svc.Request"),
					},
				},
			},
		},
	}

	reg, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{dep, main},
	})
	attest.Ok(t, err, attest.Fatal())

	var req protoreflect.MessageDescriptor
	reg.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Path() == "svc.proto" {
			req = fd.Messages().Get(0)
		}
		return true
	})

	crossPkgField := req.Fields().Get(0)
	samePkgField := req.Fields().Get(1)

	attest.Equal(t, fieldTypeName(crossPkgField), "other.OtherMsg",
		attest.Sprintf("cross-package type should be FQN"))
	attest.Equal(t, fieldTypeName(samePkgField), "Request",
		attest.Sprintf("same-package type should be short name"))
}

func TestRenderPackage_Oneof(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("oneof.proto"),
		Syntax:  new("proto3"),
		Package: new("oneof"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Msg"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("standalone"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				{Name: new("choice_a"), Number: new(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), OneofIndex: new(int32(0))},
				{Name: new("choice_b"), Number: new(int32(3)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), OneofIndex: new(int32(0))},
			},
			OneofDecl: []*descriptorpb.OneofDescriptorProto{
				{Name: new("kind")},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"oneof.proto": true})
	pkg := items[0].(*docsPackage)
	out := renderPackage(pkg, false)

	attest.True(t, strings.Contains(out, "standalone"), attest.Sprintf("standalone field missing: %q", out))
	attest.True(t, strings.Contains(out, "oneof kind"), attest.Sprintf("oneof block header missing: %q", out))
	attest.True(t, strings.Contains(out, "choice_a"), attest.Sprintf("oneof field missing: %q", out))
	attest.True(t, strings.Contains(out, "choice_b"), attest.Sprintf("oneof field missing: %q", out))
	// standalone should appear before the oneof block
	attest.True(t, strings.Index(out, "standalone") < strings.Index(out, "oneof kind"),
		attest.Sprintf("standalone should precede oneof block"))
}

func TestRenderPackage_Reserved(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("reserved.proto"),
		Syntax:  new("proto2"),
		Package: new("res"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Msg"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("active"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
			ReservedRange: []*descriptorpb.DescriptorProto_ReservedRange{
				{Start: new(int32(2)), End: new(int32(5))},  // 2 to 4
				{Start: new(int32(9)), End: new(int32(10))}, // single: 9
			},
			ReservedName: []string{"old_field", "legacy"},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"reserved.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "reserved 2 to 4"), attest.Sprintf("range missing: %q", out))
	attest.True(t, strings.Contains(out, "reserved 9"), attest.Sprintf("single reserved missing: %q", out))
	attest.True(t, strings.Contains(out, `"old_field"`), attest.Sprintf("reserved name missing: %q", out))
	attest.True(t, strings.Contains(out, `"legacy"`), attest.Sprintf("reserved name missing: %q", out))
}

func TestRenderPackage_NestedMessages(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("nested.proto"),
		Syntax:  new("proto3"),
		Package: new("nest"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Outer"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("x"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
			NestedType: []*descriptorpb.DescriptorProto{{
				Name: new("Inner"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: new("y"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
				},
			}},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"nested.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "Outer"), attest.Sprintf("Outer missing: %q", out))
	attest.True(t, strings.Contains(out, "Outer.Inner"), attest.Sprintf("Outer.Inner missing: %q", out))
	attest.True(t, strings.Contains(out, "y"), attest.Sprintf("Inner field y missing: %q", out))
	// Outer.Inner must appear after Outer.
	attest.True(t, strings.Index(out, "Outer.Inner") > strings.Index(out, "x"),
		attest.Sprintf("nested section should follow parent fields"))
}

func TestRenderPackage_Syntax(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ syntax, want string }{
		{"proto2", "proto2"},
		{"proto3", "proto3"},
	} {
		t.Run(tc.syntax, func(t *testing.T) {
			t.Parallel()
			fdp := &descriptorpb.FileDescriptorProto{
				Name:    new("syn.proto"),
				Syntax:  new(tc.syntax),
				Package: new("syn"),
				MessageType: []*descriptorpb.DescriptorProto{
					{Name: new("M"), Field: []*descriptorpb.FieldDescriptorProto{
						{Name: new("f"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					}},
				},
			}
			files := buildTestRegistry(t, fdp)
			items := packagesFromDocs(files, map[string]bool{"syn.proto": true})
			out := renderPackage(items[0].(*docsPackage), false)
			attest.True(t, strings.Contains(out, tc.want), attest.Sprintf("syntax %q missing from %q", tc.want, out))
		})
	}
}

func TestRenderPackage_Edition(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("ed.proto"),
		Syntax:  new("editions"),
		Edition: descriptorpb.Edition_EDITION_2023.Enum(),
		Package: new("ed"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("M"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("f"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			}},
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"ed.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, `edition = "2023";`), attest.Sprintf("edition line missing: %q", out))
	attest.False(t, strings.Contains(out, "syntax ="), attest.Sprintf("syntax line should not be present: %q", out))
}

func TestRenderPackage_FileFeatureOverrides(t *testing.T) {
	t.Parallel()

	// A file can explicitly override a feature's default for every element
	// within it (e.g. "option features.default_symbol_visibility = ..."),
	// which is exactly what makes otherwise-unmarked messages/enums in that
	// file non-importable elsewhere. FeatureSet fields are sparse -- only
	// populated when explicitly set at this exact scope -- so this should
	// show only for a file that actually set it, not for every Editions file.
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("feat.proto"),
		Syntax:  new("editions"),
		Edition: descriptorpb.Edition_EDITION_2024.Enum(),
		Package: new("feat"),
		Options: &descriptorpb.FileOptions{
			Features: &descriptorpb.FeatureSet{
				DefaultSymbolVisibility: descriptorpb.FeatureSet_VisibilityFeature_LOCAL_ALL.Enum(),
			},
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"feat.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "features.default_symbol_visibility = LOCAL_ALL"), attest.Sprintf("file-level feature override missing: %q", out))
}

func TestRenderPackage_NoFileFeatureOverrides(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("nofeat.proto"),
		Syntax:  new("editions"),
		Edition: descriptorpb.Edition_EDITION_2023.Enum(),
		Package: new("nofeat"),
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"nofeat.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.False(t, strings.Contains(out, "features."), attest.Sprintf("no feature overrides should be shown: %q", out))
}

func TestRenderPackage_RequiredField(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("req.proto"),
		Syntax:  new("proto2"),
		Package: new("req"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("must_have"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_REQUIRED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				{Name: new("optional_field"), Number: new(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"req.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "required string"), attest.Sprintf("required keyword missing: %q", out))
	attest.True(t, strings.Contains(out, "must_have"), attest.Sprintf("field name missing: %q", out))
	attest.Equal(t, strings.Count(out, "required"), 1, attest.Sprintf("only must_have should be marked required: %q", out))

	// proto2 requires every non-required, non-repeated field to be declared
	// with an explicit "optional" keyword in source -- unlike proto3, where a
	// bare field has implicit presence and no keyword at all.
	attest.True(t, strings.Contains(out, "optional string"), attest.Sprintf("proto2 plain field missing 'optional' keyword: %q", out))
	attest.True(t, strings.Contains(out, "optional_field"), attest.Sprintf("field name missing: %q", out))
}

func TestRenderPackage_NestedEnum(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("nestedenum.proto"),
		Syntax:  new("proto3"),
		Package: new("ne"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Outer"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("x"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
			EnumType: []*descriptorpb.EnumDescriptorProto{{
				Name: new("Status"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: new("STATUS_UNSPECIFIED"), Number: new(int32(0))},
					{Name: new("STATUS_ACTIVE"), Number: new(int32(1))},
				},
			}},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"nestedenum.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "Outer.Status"), attest.Sprintf("Outer.Status missing: %q", out))
	attest.True(t, strings.Contains(out, "STATUS_ACTIVE"), attest.Sprintf("enum value missing: %q", out))
	// The nested enum should follow Outer's own fields.
	attest.True(t, strings.Index(out, "Outer.Status") > strings.Index(out, "x"),
		attest.Sprintf("nested enum should follow parent fields"))
}

func TestRenderPackage_ExtensionRanges(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("extrange.proto"),
		Syntax:  new("proto2"),
		Package: new("er"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("active"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
			ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
				{Start: new(int32(100)), End: new(int32(150))},        // 100 to 149
				{Start: new(int32(1000)), End: new(int32(536870912))}, // 1000 to max
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"extrange.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "extensions 100 to 149;"), attest.Sprintf("bounded range missing: %q", out))
	attest.True(t, strings.Contains(out, "extensions 1000 to max;"), attest.Sprintf("max range missing: %q", out))
}

func TestRenderPackage_Extension(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("ext.proto"),
		Syntax:  new("proto2"),
		Package: new("ext"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Base"),
			ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
				{Start: new(int32(100)), End: new(int32(200))},
			},
		}},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     new("repeated_ext"),
				Number:   new(int32(100)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Extendee: new(".ext.Base"),
			},
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"ext.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "extend ext.Base {"), attest.Sprintf("extend block missing: %q", out))
	attest.True(t, strings.Contains(out, "repeated string"), attest.Sprintf("repeated keyword missing: %q", out))
	attest.True(t, strings.Contains(out, "repeated_ext"), attest.Sprintf("extension field name missing: %q", out))
}

func TestRenderPackage_TopLevelExtensionDeprecatedNotDuplicated(t *testing.T) {
	t.Parallel()

	// A top-level extension's section header and its "extend { ... }" body
	// both render the same FieldDescriptor's annotations (deprecated, custom
	// options) -- unlike a nested extension, which only has the body. Make
	// sure that doesn't show [deprecated] twice.
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("extdep.proto"),
		Syntax:  new("proto2"),
		Package: new("extdep"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Base"),
			ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
				{Start: new(int32(100)), End: new(int32(200))},
			},
		}},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     new("old_ext"),
				Number:   new(int32(100)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Extendee: new(".extdep.Base"),
				Options:  &descriptorpb.FieldOptions{Deprecated: new(true)},
			},
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"extdep.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.Equal(t, strings.Count(out, "deprecated"), 1, attest.Sprintf("expected exactly one deprecated marker: %q", out))
}

func TestRenderPackage_NestedExtension(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("nestedext.proto"),
		Syntax:  new("proto2"),
		Package: new("nx"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: new("Base"),
				ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
					{Start: new(int32(100)), End: new(int32(200))},
				},
			},
			{
				Name: new("Holder"),
				Extension: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     new("held_ext"),
						Number:   new(int32(100)),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Extendee: new(".nx.Base"),
					},
				},
			},
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"nestedext.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "extend nx.Base {"), attest.Sprintf("nested extend block missing: %q", out))
	attest.True(t, strings.Contains(out, "held_ext"), attest.Sprintf("nested extension field missing: %q", out))
}

func TestCustomOptionsAnnotation_Scalar(t *testing.T) {
	t.Parallel()

	extType := buildExtensionType(t, ".google.protobuf.FieldOptions", "my_label", 50001, descriptorpb.FieldDescriptorProto_TYPE_STRING, "")
	opts := &descriptorpb.FieldOptions{}
	proto.SetExtension(opts, extType, "hello")

	got := customOptionsAnnotation(opts, nil)
	attest.True(t, strings.Contains(got, "testext.my_label"), attest.Sprintf("extension name missing: %q", got))
	attest.True(t, strings.Contains(got, `"hello"`), attest.Sprintf("extension value missing: %q", got))
}

func TestCustomOptionsAnnotation_Message(t *testing.T) {
	t.Parallel()

	extType := buildExtensionType(t, ".google.protobuf.MethodOptions", "my_detail", 50002, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".testext.Detail")
	detailMsg := dynamicpb.NewMessage(extType.TypeDescriptor().Message())
	detailMsg.Set(detailMsg.Descriptor().Fields().ByName("name"), protoreflect.ValueOfString("widget"))
	opts := &descriptorpb.MethodOptions{}
	proto.SetExtension(opts, extType, detailMsg)

	got := customOptionsAnnotation(opts, nil)
	attest.True(t, strings.Contains(got, "testext.my_detail"), attest.Sprintf("extension name missing: %q", got))
	attest.True(t, strings.Contains(got, "widget"), attest.Sprintf("nested message value missing: %q", got))
}

// TestCustomOptionsAnnotation_AnyResolvesDynamicType covers a custom option
// whose value is a google.protobuf.Any wrapping a message type that only
// exists dynamically (no generated Go package) -- exactly the situation for
// a real third-party BSR module's custom option. prototext can only expand
// an Any to its compact "[type.url]{...}" form if given a resolver that
// knows about the wrapped type; without one it silently falls back to the
// Any's own raw type_url/value fields.
func TestCustomOptionsAnnotation_AnyResolvesDynamicType(t *testing.T) {
	t.Parallel()

	descProtoFDP := protodesc.ToFileDescriptorProto((&descriptorpb.FileOptions{}).ProtoReflect().Descriptor().ParentFile())
	anyFDP := protodesc.ToFileDescriptorProto((&anypb.Any{}).ProtoReflect().Descriptor().ParentFile())

	extFDP := &descriptorpb.FileDescriptorProto{
		Name:       new("anyext.proto"),
		Syntax:     new("proto3"),
		Package:    new("anyext"),
		Dependency: []string{"google/protobuf/descriptor.proto", "google/protobuf/any.proto"},
		Extension: []*descriptorpb.FieldDescriptorProto{{
			Name:     new("detail"),
			Number:   new(int32(50020)),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: new(".google.protobuf.Any"),
			Extendee: new(".google.protobuf.MethodOptions"),
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Detail"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("name"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
		}},
	}

	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{descProtoFDP, anyFDP, extFDP}})
	attest.Ok(t, err, attest.Fatal())

	extDesc, err := files.FindDescriptorByName("anyext.detail")
	attest.Ok(t, err, attest.Fatal())
	extType := dynamicpb.NewExtensionType(extDesc.(protoreflect.ExtensionDescriptor))

	detailDesc, err := files.FindDescriptorByName("anyext.Detail")
	attest.Ok(t, err, attest.Fatal())
	detailMsg := dynamicpb.NewMessage(detailDesc.(protoreflect.MessageDescriptor))
	detailMsg.Set(detailMsg.Descriptor().Fields().ByName("name"), protoreflect.ValueOfString("widget"))
	detailBytes, err := proto.Marshal(detailMsg)
	attest.Ok(t, err, attest.Fatal())

	anyDesc, err := files.FindDescriptorByName("google.protobuf.Any")
	attest.Ok(t, err, attest.Fatal())
	anyMsg := dynamicpb.NewMessage(anyDesc.(protoreflect.MessageDescriptor))
	anyFields := anyMsg.Descriptor().Fields()
	anyMsg.Set(anyFields.ByName("type_url"), protoreflect.ValueOfString("type.googleapis.com/anyext.Detail"))
	anyMsg.Set(anyFields.ByName("value"), protoreflect.ValueOfBytes(detailBytes))

	opts := &descriptorpb.MethodOptions{}
	proto.SetExtension(opts, extType, anyMsg.Interface())

	// Without a resolver, prototext can't expand the Any and falls back to
	// its own raw fields -- the inner message stays serialized bytes rather
	// than the expanded `name:"widget"` form (its bytes happen to contain
	// the readable substring "widget", so check for the unexpanded field
	// shape rather than mere absence of that substring).
	withoutResolver := customOptionsAnnotation(opts, nil)
	attest.True(t, strings.Contains(withoutResolver, "type_url"), attest.Sprintf("expected raw Any fields without a resolver: %q", withoutResolver))
	attest.False(t, strings.Contains(withoutResolver, `name:"widget"`), attest.Sprintf("inner message shouldn't be expanded without a resolver: %q", withoutResolver))

	got := customOptionsAnnotation(opts, dynamicpb.NewTypes(files))
	attest.True(t, strings.Contains(got, "anyext.detail"), attest.Sprintf("extension name missing: %q", got))
	attest.True(t, strings.Contains(got, `name:"widget"`), attest.Sprintf("Any value should expand to show the inner message's fields: %q", got))
	attest.False(t, strings.Contains(got, "type_url"), attest.Sprintf("Any should be expanded, not shown as raw type_url/value: %q", got))
}

func TestCustomOptionsAnnotation_NoOptions(t *testing.T) {
	t.Parallel()

	got := customOptionsAnnotation(&descriptorpb.FieldOptions{}, nil)
	attest.Equal(t, got, "")
}

func TestCustomOptionsAnnotation_ExcludesStandardFields(t *testing.T) {
	t.Parallel()

	opts := &descriptorpb.FieldOptions{Deprecated: new(true)}
	got := customOptionsAnnotation(opts, nil)
	attest.Equal(t, got, "", attest.Sprintf("standard (non-extension) fields should not appear: %q", got))
}

func TestRenderPackage_CustomOptions(t *testing.T) {
	t.Parallel()

	fieldExt := buildExtensionType(t, ".google.protobuf.FieldOptions", "validate_min", 50001, descriptorpb.FieldDescriptorProto_TYPE_INT32, "")
	msgExt := buildExtensionType(t, ".google.protobuf.MessageOptions", "table_name", 50002, descriptorpb.FieldDescriptorProto_TYPE_STRING, "")
	methodExt := buildExtensionType(t, ".google.protobuf.MethodOptions", "http_get", 50003, descriptorpb.FieldDescriptorProto_TYPE_STRING, "")
	enumExt := buildExtensionType(t, ".google.protobuf.EnumOptions", "enum_tag", 50004, descriptorpb.FieldDescriptorProto_TYPE_STRING, "")
	enumValueExt := buildExtensionType(t, ".google.protobuf.EnumValueOptions", "value_tag", 50005, descriptorpb.FieldDescriptorProto_TYPE_STRING, "")
	serviceExt := buildExtensionType(t, ".google.protobuf.ServiceOptions", "service_tag", 50006, descriptorpb.FieldDescriptorProto_TYPE_STRING, "")

	fieldOpts := &descriptorpb.FieldOptions{Deprecated: new(true)}
	proto.SetExtension(fieldOpts, fieldExt, int32(1))

	msgOpts := &descriptorpb.MessageOptions{}
	proto.SetExtension(msgOpts, msgExt, "widgets")

	methodOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(methodOpts, methodExt, "/v1/widgets")

	enumOpts := &descriptorpb.EnumOptions{}
	proto.SetExtension(enumOpts, enumExt, "state")

	enumValueOpts := &descriptorpb.EnumValueOptions{}
	proto.SetExtension(enumValueOpts, enumValueExt, "default-state")

	serviceOpts := &descriptorpb.ServiceOptions{}
	proto.SetExtension(serviceOpts, serviceExt, "widget-api")

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("custom.proto"),
		Syntax:  new("proto3"),
		Package: new("custom"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:    new("Widget"),
				Options: msgOpts,
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: new("count"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Options: fieldOpts},
				},
			},
			{Name: new("Empty")},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name:    new("Status"),
			Options: enumOpts,
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: new("STATUS_UNSPECIFIED"), Number: new(int32(0)), Options: enumValueOpts},
			},
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name:    new("WidgetService"),
			Options: serviceOpts,
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       new("GetWidget"),
				InputType:  new(".custom.Empty"),
				OutputType: new(".custom.Widget"),
				Options:    methodOpts,
			}},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"custom.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "testext.table_name"), attest.Sprintf("message custom option missing: %q", out))
	attest.True(t, strings.Contains(out, "widgets"), attest.Sprintf("message custom option value missing: %q", out))
	attest.True(t, strings.Contains(out, "testext.validate_min"), attest.Sprintf("field custom option missing: %q", out))
	attest.True(t, strings.Contains(out, "testext.http_get"), attest.Sprintf("method custom option missing: %q", out))
	attest.True(t, strings.Contains(out, "/v1/widgets"), attest.Sprintf("method custom option value missing: %q", out))
	attest.True(t, strings.Contains(out, "testext.enum_tag"), attest.Sprintf("enum custom option missing: %q", out))
	attest.True(t, strings.Contains(out, "testext.value_tag"), attest.Sprintf("enum value custom option missing: %q", out))
	attest.True(t, strings.Contains(out, "testext.service_tag"), attest.Sprintf("service custom option missing: %q", out))
	// deprecated and the custom option should both show, without duplication.
	attest.Equal(t, strings.Count(out, "deprecated"), 1, attest.Sprintf("expected exactly one deprecated marker: %q", out))
}

func TestRenderField_DefaultValue(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("def.proto"),
		Syntax:  new("proto2"),
		Package: new("def"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: new("Color"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: new("RED"), Number: new(int32(0))},
				{Name: new("BLUE"), Number: new(int32(1))},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("label"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), DefaultValue: new("hello")},
				{Name: new("count"), Number: new(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), DefaultValue: new("42")},
				{Name: new("active"), Number: new(int32(3)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(), DefaultValue: new("true")},
				{Name: new("color"), Number: new(int32(4)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), TypeName: new(".def.Color"), DefaultValue: new("BLUE")},
				{Name: new("plain"), Number: new(int32(5)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"def.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, `[default = "hello"]`), attest.Sprintf("string default missing: %q", out))
	attest.True(t, strings.Contains(out, "[default = 42]"), attest.Sprintf("int default missing: %q", out))
	attest.True(t, strings.Contains(out, "[default = true]"), attest.Sprintf("bool default missing: %q", out))
	attest.True(t, strings.Contains(out, "[default = BLUE]"), attest.Sprintf("enum default missing: %q", out))
	attest.Equal(t, strings.Count(out, "[default"), 4, attest.Sprintf("plain field should not get a default annotation: %q", out))
}

func TestRenderField_JSONNameOverride(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("j.proto"),
		Syntax:  new("proto3"),
		Package: new("j"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				// No explicit json_name -- protodesc derives "myField".
				{Name: new("my_field"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				// Explicit json_name that genuinely differs from the derived name.
				{Name: new("other_field"), Number: new(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), JsonName: new("customName")},
				// Real compilers always populate json_name, even when the user
				// didn't write an override -- this one happens to match the
				// derived name and should NOT be flagged as an override.
				{Name: new("same_as_derived"), Number: new(int32(3)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), JsonName: new("sameAsDerived")},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"j.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, `[json_name = "customName"]`), attest.Sprintf("json_name override missing: %q", out))
	attest.Equal(t, strings.Count(out, "json_name"), 1, attest.Sprintf("only the genuine override should be flagged: %q", out))
}

func TestRenderField_ExplicitPacked(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("packed.proto"),
		Syntax:  new("proto2"),
		Package: new("packed"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				// No explicit packed option -- should not be annotated even
				// though IsPacked() has a well-defined effective value.
				{Name: new("no_opt"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
				{Name: new("explicit_true"), Number: new(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Options: &descriptorpb.FieldOptions{Packed: new(true)}},
				{Name: new("explicit_false"), Number: new(int32(3)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Options: &descriptorpb.FieldOptions{Packed: new(false)}},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"packed.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "[packed = true]"), attest.Sprintf("explicit packed=true missing: %q", out))
	attest.True(t, strings.Contains(out, "[packed = false]"), attest.Sprintf("explicit packed=false missing: %q", out))
	attest.Equal(t, strings.Count(out, "[packed"), 2, attest.Sprintf("field without an explicit option should not be annotated: %q", out))
}

func TestRenderField_EditionsExplicitRepeatedFieldEncoding(t *testing.T) {
	t.Parallel()

	// FieldOptions.packed "is prohibited in Editions, but the
	// repeated_field_encoding feature can be used to control the behavior"
	// instead (per its doc comment in descriptor.proto) -- so a naive check
	// of only the "packed" option can never fire for an Editions file, and
	// an explicit per-field wire-encoding override would render invisibly.
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("edpacked.proto"),
		Syntax:  new("editions"),
		Edition: descriptorpb.Edition_EDITION_2023.Enum(),
		Package: new("edpacked"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				// No explicit override -- should not be annotated even though
				// it resolves to a well-defined effective packed/expanded value.
				{Name: new("no_opt"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
				{
					Name: new("explicit_expanded"), Number: new(int32(2)),
					Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
					Options: &descriptorpb.FieldOptions{
						Features: &descriptorpb.FeatureSet{RepeatedFieldEncoding: descriptorpb.FeatureSet_EXPANDED.Enum()},
					},
				},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"edpacked.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "[features.repeated_field_encoding = EXPANDED]"), attest.Sprintf("explicit repeated_field_encoding override missing: %q", out))
	attest.Equal(t, strings.Count(out, "repeated_field_encoding"), 1, attest.Sprintf("field without an explicit override should not be annotated: %q", out))
}

func TestRenderField_DebugRedact(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("redact.proto"),
		Syntax:  new("proto3"),
		Package: new("redact"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("plain"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				{Name: new("password"), Number: new(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Options: &descriptorpb.FieldOptions{DebugRedact: new(true)}},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"redact.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "[debug_redact]"), attest.Sprintf("debug_redact annotation missing: %q", out))
	attest.Equal(t, strings.Count(out, "debug_redact"), 1, attest.Sprintf("only password should be annotated: %q", out))
}

func TestRenderPackage_EnumAlias(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("alias.proto"),
		Syntax:  new("proto3"),
		Package: new("alias"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name:    new("Color"),
			Options: &descriptorpb.EnumOptions{AllowAlias: new(true)},
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: new("RED"), Number: new(int32(0))},
				{Name: new("CRIMSON"), Number: new(int32(0))}, // alias of RED
				{Name: new("BLUE"), Number: new(int32(1))},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"alias.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "CRIMSON"), attest.Sprintf("alias value missing: %q", out))
	attest.True(t, strings.Contains(out, "alias of RED"), attest.Sprintf("alias annotation missing: %q", out))
	// RED and BLUE are canonical -- should not be flagged as aliases.
	attest.Equal(t, strings.Count(out, "alias of"), 1, attest.Sprintf("only CRIMSON should be flagged: %q", out))
}

func TestRenderPackage_NestedEnumAlias(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("nestedalias.proto"),
		Syntax:  new("proto3"),
		Package: new("na"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Outer"),
			EnumType: []*descriptorpb.EnumDescriptorProto{{
				Name:    new("Color"),
				Options: &descriptorpb.EnumOptions{AllowAlias: new(true)},
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: new("RED"), Number: new(int32(0))},
					{Name: new("CRIMSON"), Number: new(int32(0))},
				},
			}},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"nestedalias.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "alias of RED"), attest.Sprintf("nested alias annotation missing: %q", out))
}

func TestRenderField_Group(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("group.proto"),
		Syntax:  new("proto2"),
		Package: new("grp"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("result"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_GROUP.Enum(), TypeName: new(".grp.M.Result")},
				{Name: new("results"), Number: new(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_GROUP.Enum(), TypeName: new(".grp.M.Results")},
			},
			NestedType: []*descriptorpb.DescriptorProto{
				{
					Name: new("Result"),
					Field: []*descriptorpb.FieldDescriptorProto{
						{Name: new("url"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					},
				},
				{
					Name: new("Results"),
					Field: []*descriptorpb.FieldDescriptorProto{
						{Name: new("url"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					},
				},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"group.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "group Result"), attest.Sprintf("singular group field missing 'group' keyword: %q", out))
	attest.True(t, strings.Contains(out, "repeated group Results"), attest.Sprintf("repeated group field missing 'repeated group': %q", out))
	attest.True(t, strings.Contains(out, "result = 1"), attest.Sprintf("group field name/number missing: %q", out))
}

func TestRenderField_EditionsDelimitedEncoding(t *testing.T) {
	t.Parallel()

	// Under Editions, message_encoding=DELIMITED reuses group-like wire
	// encoding but through ordinary message-field source syntax -- there is
	// no "group" keyword in Editions (files using proto3 or Editions syntax
	// aren't allowed GroupDecl at all). protoreflect still reports
	// Kind()==GroupKind for these fields, so naively keying off Kind() alone
	// would render the invalid/misleading "group Detail detail = 1" instead
	// of the real source form "Detail detail = 1".
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("delim.proto"),
		Syntax:  new("editions"),
		Edition: descriptorpb.Edition_EDITION_2023.Enum(),
		Package: new("delim"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{
					Name:     new("detail"),
					Number:   new(int32(1)),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
					TypeName: new(".delim.Detail"),
					Options: &descriptorpb.FieldOptions{
						Features: &descriptorpb.FeatureSet{MessageEncoding: descriptorpb.FeatureSet_DELIMITED.Enum()},
					},
				},
			},
		}, {
			Name: new("Detail"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("url"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"delim.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.False(t, strings.Contains(out, "group"), attest.Sprintf("Editions delimited field should not use the (invalid) 'group' keyword: %q", out))
	attest.True(t, strings.Contains(out, "Detail"), attest.Sprintf("delimited field's message type missing: %q", out))
	attest.True(t, strings.Contains(out, "detail = 1"), attest.Sprintf("delimited field name/number missing: %q", out))
}

func TestRenderPackage_Proto3OptionalField(t *testing.T) {
	t.Parallel()

	// proto3 `optional` fields compile to a hidden "synthetic" oneof
	// containing just that one field -- this must not leak into the docs
	// as a visible oneof block.
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("opt.proto"),
		Syntax:  new("proto3"),
		Package: new("opt"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("name"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Proto3Optional: new(true), OneofIndex: new(int32(0))},
			},
			OneofDecl: []*descriptorpb.OneofDescriptorProto{
				{Name: new("_name")},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"opt.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "optional string"), attest.Sprintf("optional keyword missing: %q", out))
	attest.True(t, strings.Contains(out, "name = 1"), attest.Sprintf("field missing: %q", out))
	attest.False(t, strings.Contains(out, "oneof"), attest.Sprintf("synthetic oneof should not render as a oneof block: %q", out))
	attest.False(t, strings.Contains(out, "_name"), attest.Sprintf("synthetic oneof name should never leak into docs: %q", out))
}

func TestRenderPackage_EnumReserved(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("enumres.proto"),
		Syntax:  new("proto3"),
		Package: new("enumres"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: new("Status"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: new("STATUS_UNSPECIFIED"), Number: new(int32(0))},
			},
			ReservedRange: []*descriptorpb.EnumDescriptorProto_EnumReservedRange{
				{Start: new(int32(5)), End: new(int32(10))},  // inclusive: 5 to 10
				{Start: new(int32(20)), End: new(int32(20))}, // single: 20
			},
			ReservedName: []string{"OLD_STATUS", "LEGACY"},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"enumres.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "reserved 5 to 10;"), attest.Sprintf("enum reserved range missing: %q", out))
	attest.True(t, strings.Contains(out, "reserved 20;"), attest.Sprintf("enum single reserved missing: %q", out))
	attest.True(t, strings.Contains(out, `"OLD_STATUS"`), attest.Sprintf("enum reserved name missing: %q", out))
	attest.True(t, strings.Contains(out, `"LEGACY"`), attest.Sprintf("enum reserved name missing: %q", out))
}

func TestRenderPackage_ExtensionRangeCustomOption(t *testing.T) {
	t.Parallel()

	rangeExt := buildExtensionType(t, ".google.protobuf.ExtensionRangeOptions", "range_owner", 50001, descriptorpb.FieldDescriptorProto_TYPE_STRING, "")
	rangeOpts := &descriptorpb.ExtensionRangeOptions{}
	proto.SetExtension(rangeOpts, rangeExt, "team-foo")

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("extrangeopt.proto"),
		Syntax:  new("proto2"),
		Package: new("ero"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
				{Start: new(int32(100)), End: new(int32(200)), Options: rangeOpts},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"extrangeopt.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "extensions 100 to 199;"), attest.Sprintf("extension range missing: %q", out))
	attest.True(t, strings.Contains(out, "testext.range_owner"), attest.Sprintf("extension range custom option missing: %q", out))
	attest.True(t, strings.Contains(out, "team-foo"), attest.Sprintf("extension range custom option value missing: %q", out))
}

func TestRenderPackage_ClosedEnum(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		syntax     string
		wantClosed bool
	}{
		{"proto2", true},
		{"proto3", false},
	} {
		t.Run(tc.syntax, func(t *testing.T) {
			t.Parallel()
			fdp := &descriptorpb.FileDescriptorProto{
				Name:    new("closed.proto"),
				Syntax:  new(tc.syntax),
				Package: new("closed"),
				EnumType: []*descriptorpb.EnumDescriptorProto{{
					Name:  new("E"),
					Value: []*descriptorpb.EnumValueDescriptorProto{{Name: new("E_UNSPECIFIED"), Number: new(int32(0))}},
				}},
			}
			files := buildTestRegistry(t, fdp)
			items := packagesFromDocs(files, map[string]bool{"closed.proto": true})
			out := renderPackage(items[0].(*docsPackage), false)
			attest.Equal(t, strings.Contains(out, "[closed]"), tc.wantClosed, attest.Sprintf("closed=%v mismatch: %q", tc.wantClosed, out))
		})
	}
}

func TestRenderPackage_SymbolVisibility(t *testing.T) {
	t.Parallel()

	// Symbol visibility (Editions 2024+) restricts whether a message/enum
	// can be imported by other files. DescriptorProto/EnumDescriptorProto's
	// Visibility field is left VISIBILITY_UNSET unless the source explicitly
	// wrote "local" or "export" on that exact declaration -- otherwise it
	// resolves from the file's default_symbol_visibility feature (or EXPORT
	// pre-2024) -- so checking it directly tells us whether this was an
	// explicit, author-written keyword.
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("vis.proto"),
		Syntax:  new("editions"),
		Edition: descriptorpb.Edition_EDITION_2024.Enum(),
		Package: new("vis"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("LocalMsg"), Visibility: descriptorpb.SymbolVisibility_VISIBILITY_LOCAL.Enum()},
			{Name: new("ExportMsg"), Visibility: descriptorpb.SymbolVisibility_VISIBILITY_EXPORT.Enum()},
			{Name: new("PlainMsg")},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name:       new("LocalEnum"),
				Visibility: descriptorpb.SymbolVisibility_VISIBILITY_LOCAL.Enum(),
				Value:      []*descriptorpb.EnumValueDescriptorProto{{Name: new("LOCAL_ENUM_UNSPECIFIED"), Number: new(int32(0))}},
			},
			{
				Name:  new("PlainEnum"),
				Value: []*descriptorpb.EnumValueDescriptorProto{{Name: new("PLAIN_ENUM_UNSPECIFIED"), Number: new(int32(0))}},
			},
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"vis.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "[local]"), attest.Sprintf("local annotation missing: %q", out))
	attest.True(t, strings.Contains(out, "[export]"), attest.Sprintf("export annotation missing: %q", out))
	attest.Equal(t, strings.Count(out, "[local]"), 2, attest.Sprintf("expected LocalMsg and LocalEnum to both be annotated: %q", out))
	attest.Equal(t, strings.Count(out, "[export]"), 1, attest.Sprintf("expected only ExportMsg to be annotated: %q", out))
}

func TestRenderField_ExtensionJSONNameNotFlagged(t *testing.T) {
	t.Parallel()

	// Extension fields get an automatic bracketed JSON name per the
	// protobuf spec ("[pkg.field]"), which will always differ from the
	// plain camelCase derivation used for regular fields. That's not an
	// author-written override and shouldn't be flagged as one.
	descProtoFDP := protodesc.ToFileDescriptorProto((&descriptorpb.FileOptions{}).ProtoReflect().Descriptor().ParentFile())
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       new("extjson.proto"),
		Syntax:     new("proto2"),
		Package:    new("extjson"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     new("my_ext"),
				Number:   new(int32(50001)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Extendee: new(".google.protobuf.FieldOptions"),
			},
		},
	}

	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{descProtoFDP, fdp},
	})
	attest.Ok(t, err, attest.Fatal())

	items := packagesFromDocs(files, map[string]bool{"extjson.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.False(t, strings.Contains(out, "json_name"), attest.Sprintf("extension field's automatic bracketed json_name should not be flagged as an override: %q", out))
}
