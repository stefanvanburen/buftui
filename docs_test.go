package main

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"go.akshayshah.org/attest"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
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
