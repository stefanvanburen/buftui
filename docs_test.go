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
)

// ptr is a generic helper for proto scalar pointer fields.
func ptr[T any](v T) *T { return &v }

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
		Name:       ptr("testext.proto"),
		Syntax:     ptr("proto3"),
		Package:    ptr("testext"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		Extension: []*descriptorpb.FieldDescriptorProto{{
			Name:     ptr(name),
			Number:   ptr(number),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     typ.Enum(),
			TypeName: nonEmptyPtr(typeName),
			Extendee: ptr(extendee),
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("Detail"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("name"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
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
		Name:    ptr("test.proto"),
		Syntax:  ptr("proto2"),
		Package: ptr("test"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: ptr("Zebra"),
				ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
					{Start: ptr(int32(1000)), End: ptr(int32(2000))},
				},
			},
			{Name: ptr("Apple")},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name: ptr("Status"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: ptr("STATUS_UNSPECIFIED"), Number: ptr(int32(0))},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("ZebraService")},
			{Name: ptr("AppleService")},
		},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     ptr("my_ext"),
				Number:   ptr(int32(1000)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Extendee: ptr(".test.Zebra"),
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
		Name:    ptr("z.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("z.pkg"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("ZMsg")},
		},
	}
	fdp2 := &descriptorpb.FileDescriptorProto{
		Name:    ptr("a.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("a.pkg"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("AMsg")},
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
		Name:    ptr("dep.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("dep"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("DepMessage")},
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
		Name:    ptr("fqn.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("z"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Apple")},  // FQN: z.Apple
			{Name: ptr("Mango")},  // FQN: z.Mango
			{Name: ptr("Banana")}, // FQN: z.Banana
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
		Name:    ptr("deprecated.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("dep"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:    ptr("OldMessage"),
				Options: &descriptorpb.MessageOptions{Deprecated: &deprecated},
			},
			{Name: ptr("NewMessage")},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name:    ptr("OldEnum"),
				Options: &descriptorpb.EnumOptions{Deprecated: &deprecated},
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: ptr("OLD_ENUM_UNSPECIFIED"), Number: ptr(int32(0))},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name:    ptr("OldService"),
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
		Name:    ptr("desc.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("desc"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("A")},
			{Name: ptr("B")},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name: ptr("E"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: ptr("E_UNSPECIFIED"), Number: ptr(int32(0))},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("Svc")},
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
		Name:    ptr("svc.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("svc"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Req")},
			{Name: ptr("Resp")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: ptr("Svc"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{
					Name:       ptr("Plain"),
					InputType:  ptr(".svc.Req"),
					OutputType: ptr(".svc.Resp"),
				},
				{
					Name:       ptr("ReadOnly"),
					InputType:  ptr(".svc.Req"),
					OutputType: ptr(".svc.Resp"),
					Options:    &descriptorpb.MethodOptions{IdempotencyLevel: &noSideEffects},
				},
				{
					Name:       ptr("Idempotent"),
					InputType:  ptr(".svc.Req"),
					OutputType: ptr(".svc.Resp"),
					Options:    &descriptorpb.MethodOptions{IdempotencyLevel: &idempotent},
				},
				{
					Name:       ptr("OldMethod"),
					InputType:  ptr(".svc.Req"),
					OutputType: ptr(".svc.Resp"),
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

	plain := renderMethod(svc.Methods().Get(0), typeStyle, dim, comment)
	readOnly := renderMethod(svc.Methods().Get(1), typeStyle, dim, comment)
	idempotentOut := renderMethod(svc.Methods().Get(2), typeStyle, dim, comment)
	oldMethod := renderMethod(svc.Methods().Get(3), typeStyle, dim, comment)

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
		Name:    ptr("map.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("mypkg"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Inner")},
			{
				Name: ptr("Outer"),
				Field: []*descriptorpb.FieldDescriptorProto{
					// map<string, Inner> — proto encodes as a synthetic map entry message
					{
						Name:     ptr("my_map"),
						Number:   ptr(int32(1)),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: ptr(".mypkg.Outer.MyMapEntry"),
					},
				},
				NestedType: []*descriptorpb.DescriptorProto{
					{
						Name:    ptr("MyMapEntry"),
						Options: &descriptorpb.MessageOptions{MapEntry: ptr(true)},
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:   ptr("key"),
								Number: ptr(int32(1)),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
							{
								Name:     ptr("value"),
								Number:   ptr(int32(2)),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
								TypeName: ptr(".mypkg.Inner"),
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
		Name:    ptr("other.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("other"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("OtherMsg")},
		},
	}
	main := &descriptorpb.FileDescriptorProto{
		Name:       ptr("svc.proto"),
		Syntax:     ptr("proto3"),
		Package:    ptr("svc"),
		Dependency: []string{"other.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: ptr("Request"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     ptr("other"),
						Number:   ptr(int32(1)),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: ptr(".other.OtherMsg"),
					},
					{
						Name:     ptr("same_pkg"),
						Number:   ptr(int32(2)),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: ptr(".svc.Request"),
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
		Name:    ptr("oneof.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("oneof"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("Msg"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("standalone"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				{Name: ptr("choice_a"), Number: ptr(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), OneofIndex: ptr(int32(0))},
				{Name: ptr("choice_b"), Number: ptr(int32(3)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), OneofIndex: ptr(int32(0))},
			},
			OneofDecl: []*descriptorpb.OneofDescriptorProto{
				{Name: ptr("kind")},
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
		Name:    ptr("reserved.proto"),
		Syntax:  ptr("proto2"),
		Package: ptr("res"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("Msg"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("active"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
			ReservedRange: []*descriptorpb.DescriptorProto_ReservedRange{
				{Start: ptr(int32(2)), End: ptr(int32(5))},  // 2 to 4
				{Start: ptr(int32(9)), End: ptr(int32(10))}, // single: 9
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
		Name:    ptr("nested.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("nest"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("Outer"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("x"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
			NestedType: []*descriptorpb.DescriptorProto{{
				Name: ptr("Inner"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: ptr("y"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
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
				Name:    ptr("syn.proto"),
				Syntax:  ptr(tc.syntax),
				Package: ptr("syn"),
				MessageType: []*descriptorpb.DescriptorProto{
					{Name: ptr("M"), Field: []*descriptorpb.FieldDescriptorProto{
						{Name: ptr("f"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
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
		Name:    ptr("ed.proto"),
		Syntax:  ptr("editions"),
		Edition: descriptorpb.Edition_EDITION_2023.Enum(),
		Package: ptr("ed"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("M"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("f"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			}},
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"ed.proto": true})
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, `edition = "2023";`), attest.Sprintf("edition line missing: %q", out))
	attest.False(t, strings.Contains(out, "syntax ="), attest.Sprintf("syntax line should not be present: %q", out))
}

func TestRenderPackage_RequiredField(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    ptr("req.proto"),
		Syntax:  ptr("proto2"),
		Package: ptr("req"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("must_have"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_REQUIRED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				{Name: ptr("optional_field"), Number: ptr(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
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
		Name:    ptr("nestedenum.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("ne"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("Outer"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("x"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
			EnumType: []*descriptorpb.EnumDescriptorProto{{
				Name: ptr("Status"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: ptr("STATUS_UNSPECIFIED"), Number: ptr(int32(0))},
					{Name: ptr("STATUS_ACTIVE"), Number: ptr(int32(1))},
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
		Name:    ptr("extrange.proto"),
		Syntax:  ptr("proto2"),
		Package: ptr("er"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("active"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
			ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
				{Start: ptr(int32(100)), End: ptr(int32(150))},        // 100 to 149
				{Start: ptr(int32(1000)), End: ptr(int32(536870912))}, // 1000 to max
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
		Name:    ptr("ext.proto"),
		Syntax:  ptr("proto2"),
		Package: ptr("ext"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("Base"),
			ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
				{Start: ptr(int32(100)), End: ptr(int32(200))},
			},
		}},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     ptr("repeated_ext"),
				Number:   ptr(int32(100)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Extendee: ptr(".ext.Base"),
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

func TestRenderPackage_NestedExtension(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    ptr("nestedext.proto"),
		Syntax:  ptr("proto2"),
		Package: ptr("nx"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: ptr("Base"),
				ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
					{Start: ptr(int32(100)), End: ptr(int32(200))},
				},
			},
			{
				Name: ptr("Holder"),
				Extension: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     ptr("held_ext"),
						Number:   ptr(int32(100)),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Extendee: ptr(".nx.Base"),
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

	got := customOptionsAnnotation(opts)
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

	got := customOptionsAnnotation(opts)
	attest.True(t, strings.Contains(got, "testext.my_detail"), attest.Sprintf("extension name missing: %q", got))
	attest.True(t, strings.Contains(got, "widget"), attest.Sprintf("nested message value missing: %q", got))
}

func TestCustomOptionsAnnotation_NoOptions(t *testing.T) {
	t.Parallel()

	got := customOptionsAnnotation(&descriptorpb.FieldOptions{})
	attest.Equal(t, got, "")
}

func TestCustomOptionsAnnotation_ExcludesStandardFields(t *testing.T) {
	t.Parallel()

	opts := &descriptorpb.FieldOptions{Deprecated: ptr(true)}
	got := customOptionsAnnotation(opts)
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

	fieldOpts := &descriptorpb.FieldOptions{Deprecated: ptr(true)}
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
		Name:    ptr("custom.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("custom"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:    ptr("Widget"),
				Options: msgOpts,
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: ptr("count"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Options: fieldOpts},
				},
			},
			{Name: ptr("Empty")},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name:    ptr("Status"),
			Options: enumOpts,
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: ptr("STATUS_UNSPECIFIED"), Number: ptr(int32(0)), Options: enumValueOpts},
			},
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name:    ptr("WidgetService"),
			Options: serviceOpts,
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       ptr("GetWidget"),
				InputType:  ptr(".custom.Empty"),
				OutputType: ptr(".custom.Widget"),
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
		Name:    ptr("def.proto"),
		Syntax:  ptr("proto2"),
		Package: ptr("def"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: ptr("Color"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: ptr("RED"), Number: ptr(int32(0))},
				{Name: ptr("BLUE"), Number: ptr(int32(1))},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("label"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), DefaultValue: ptr("hello")},
				{Name: ptr("count"), Number: ptr(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), DefaultValue: ptr("42")},
				{Name: ptr("active"), Number: ptr(int32(3)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(), DefaultValue: ptr("true")},
				{Name: ptr("color"), Number: ptr(int32(4)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), TypeName: ptr(".def.Color"), DefaultValue: ptr("BLUE")},
				{Name: ptr("plain"), Number: ptr(int32(5)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
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
		Name:    ptr("j.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("j"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				// No explicit json_name -- protodesc derives "myField".
				{Name: ptr("my_field"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				// Explicit json_name that genuinely differs from the derived name.
				{Name: ptr("other_field"), Number: ptr(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), JsonName: ptr("customName")},
				// Real compilers always populate json_name, even when the user
				// didn't write an override -- this one happens to match the
				// derived name and should NOT be flagged as an override.
				{Name: ptr("same_as_derived"), Number: ptr(int32(3)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), JsonName: ptr("sameAsDerived")},
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
		Name:    ptr("packed.proto"),
		Syntax:  ptr("proto2"),
		Package: ptr("packed"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				// No explicit packed option -- should not be annotated even
				// though IsPacked() has a well-defined effective value.
				{Name: ptr("no_opt"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
				{Name: ptr("explicit_true"), Number: ptr(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Options: &descriptorpb.FieldOptions{Packed: ptr(true)}},
				{Name: ptr("explicit_false"), Number: ptr(int32(3)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Options: &descriptorpb.FieldOptions{Packed: ptr(false)}},
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

func TestRenderField_DebugRedact(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    ptr("redact.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("redact"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("plain"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				{Name: ptr("password"), Number: ptr(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Options: &descriptorpb.FieldOptions{DebugRedact: ptr(true)}},
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
		Name:    ptr("alias.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("alias"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name:    ptr("Color"),
			Options: &descriptorpb.EnumOptions{AllowAlias: ptr(true)},
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: ptr("RED"), Number: ptr(int32(0))},
				{Name: ptr("CRIMSON"), Number: ptr(int32(0))}, // alias of RED
				{Name: ptr("BLUE"), Number: ptr(int32(1))},
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
		Name:    ptr("nestedalias.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("na"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("Outer"),
			EnumType: []*descriptorpb.EnumDescriptorProto{{
				Name:    ptr("Color"),
				Options: &descriptorpb.EnumOptions{AllowAlias: ptr(true)},
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: ptr("RED"), Number: ptr(int32(0))},
					{Name: ptr("CRIMSON"), Number: ptr(int32(0))},
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
		Name:    ptr("group.proto"),
		Syntax:  ptr("proto2"),
		Package: ptr("grp"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("result"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_GROUP.Enum(), TypeName: ptr(".grp.M.Result")},
				{Name: ptr("results"), Number: ptr(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_GROUP.Enum(), TypeName: ptr(".grp.M.Results")},
			},
			NestedType: []*descriptorpb.DescriptorProto{
				{
					Name: ptr("Result"),
					Field: []*descriptorpb.FieldDescriptorProto{
						{Name: ptr("url"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					},
				},
				{
					Name: ptr("Results"),
					Field: []*descriptorpb.FieldDescriptorProto{
						{Name: ptr("url"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
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

func TestRenderPackage_Proto3OptionalField(t *testing.T) {
	t.Parallel()

	// proto3 `optional` fields compile to a hidden "synthetic" oneof
	// containing just that one field -- this must not leak into the docs
	// as a visible oneof block.
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    ptr("opt.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("opt"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("name"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Proto3Optional: ptr(true), OneofIndex: ptr(int32(0))},
			},
			OneofDecl: []*descriptorpb.OneofDescriptorProto{
				{Name: ptr("_name")},
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
		Name:    ptr("enumres.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("enumres"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: ptr("Status"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: ptr("STATUS_UNSPECIFIED"), Number: ptr(int32(0))},
			},
			ReservedRange: []*descriptorpb.EnumDescriptorProto_EnumReservedRange{
				{Start: ptr(int32(5)), End: ptr(int32(10))},  // inclusive: 5 to 10
				{Start: ptr(int32(20)), End: ptr(int32(20))}, // single: 20
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
		Name:    ptr("extrangeopt.proto"),
		Syntax:  ptr("proto2"),
		Package: ptr("ero"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
				{Start: ptr(int32(100)), End: ptr(int32(200)), Options: rangeOpts},
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
				Name:    ptr("closed.proto"),
				Syntax:  ptr(tc.syntax),
				Package: ptr("closed"),
				EnumType: []*descriptorpb.EnumDescriptorProto{{
					Name:  ptr("E"),
					Value: []*descriptorpb.EnumValueDescriptorProto{{Name: ptr("E_UNSPECIFIED"), Number: ptr(int32(0))}},
				}},
			}
			files := buildTestRegistry(t, fdp)
			items := packagesFromDocs(files, map[string]bool{"closed.proto": true})
			out := renderPackage(items[0].(*docsPackage), false)
			attest.Equal(t, strings.Contains(out, "[closed]"), tc.wantClosed, attest.Sprintf("closed=%v mismatch: %q", tc.wantClosed, out))
		})
	}
}

func TestRenderField_ExtensionJSONNameNotFlagged(t *testing.T) {
	t.Parallel()

	// Extension fields get an automatic bracketed JSON name per the
	// protobuf spec ("[pkg.field]"), which will always differ from the
	// plain camelCase derivation used for regular fields. That's not an
	// author-written override and shouldn't be flagged as one.
	descProtoFDP := protodesc.ToFileDescriptorProto((&descriptorpb.FileOptions{}).ProtoReflect().Descriptor().ParentFile())
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       ptr("extjson.proto"),
		Syntax:     ptr("proto2"),
		Package:    ptr("extjson"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     ptr("my_ext"),
				Number:   ptr(int32(50001)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Extendee: ptr(".google.protobuf.FieldOptions"),
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
