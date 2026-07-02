package main

import (
	"testing"

	"go.akshayshah.org/attest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// buildCustomOptionFDS builds a FileDescriptorSet for a file that declares a
// custom extension of google.protobuf.FieldOptions (field number 50001,
// deliberately NOT registered as a generated Go type anywhere in this
// binary) and sets it on a message field. It returns the marshaled bytes,
// simulating what a real BSR module extending FieldOptions/MethodOptions
// (e.g. buf.validate, google.api.http) would produce.
func buildCustomOptionFDS(t *testing.T) []byte {
	t.Helper()

	descProtoFDP := protodesc.ToFileDescriptorProto((&descriptorpb.FileOptions{}).ProtoReflect().Descriptor().ParentFile())

	customFDP := &descriptorpb.FileDescriptorProto{
		Name:       new("custom.proto"),
		Syntax:     new("proto3"),
		Package:    new("custom"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     new("my_label"),
				Number:   new(int32(50001)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Extendee: new(".google.protobuf.FieldOptions"),
			},
		},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("labeled"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
		}},
	}

	// Resolve the extension descriptor purely from the descriptor protos
	// (no generated Go package for it exists), then set its value using a
	// dynamicpb-derived extension type -- this mirrors how a custom option
	// from a third-party BSR module would arrive: known only by descriptor,
	// never imported into this binary.
	tmpFiles, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{descProtoFDP, customFDP},
	})
	attest.Ok(t, err, attest.Fatal())
	extDesc, err := tmpFiles.FindDescriptorByName("custom.my_label")
	attest.Ok(t, err, attest.Fatal())
	extType := dynamicpb.NewExtensionType(extDesc.(protoreflect.ExtensionDescriptor))

	fieldOpts := &descriptorpb.FieldOptions{}
	proto.SetExtension(fieldOpts, extType, "hello world")
	customFDP.MessageType[0].Field[0].Options = fieldOpts

	fdsBytes, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{descProtoFDP, customFDP},
	})
	attest.Ok(t, err, attest.Fatal())
	return fdsBytes
}

func TestResolveRegistry_DecodesCustomOption(t *testing.T) {
	t.Parallel()

	fdsBytes := buildCustomOptionFDS(t)

	// A plain unmarshal (what compileDocs did before this fix) can't see the
	// custom option: field 50001 isn't a Go-registered extension anywhere in
	// this binary, so it lands in the FieldOptions' unknown fields.
	var plainFDS descriptorpb.FileDescriptorSet
	attest.Ok(t, proto.Unmarshal(fdsBytes, &plainFDS), attest.Fatal())
	plainFiles, err := protodesc.NewFiles(&plainFDS)
	attest.Ok(t, err, attest.Fatal())
	m, err := plainFiles.FindDescriptorByName("custom.M")
	attest.Ok(t, err, attest.Fatal())
	fieldOpts := m.(protoreflect.MessageDescriptor).Fields().Get(0).Options()
	sawExtension := false
	fieldOpts.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if fd.IsExtension() {
			sawExtension = true
		}
		return true
	})
	attest.False(t, sawExtension, attest.Sprintf("plain unmarshal should NOT resolve the custom option"))

	// resolveRegistry re-resolves options against the descriptor set's own
	// extension declarations, so the custom option should now be visible.
	regFiles, err := resolveRegistry(fdsBytes)
	attest.Ok(t, err, attest.Fatal())
	resolvedM, err := regFiles.FindDescriptorByName("custom.M")
	attest.Ok(t, err, attest.Fatal())
	resolvedFieldOpts := resolvedM.(protoreflect.MessageDescriptor).Fields().Get(0).Options()

	var gotValue string
	var gotName protoreflect.FullName
	resolvedFieldOpts.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.IsExtension() {
			gotName = fd.FullName()
			gotValue = v.String()
		}
		return true
	})
	attest.Equal(t, gotName, protoreflect.FullName("custom.my_label"))
	attest.Equal(t, gotValue, "hello world")
}

func TestResolveRegistry_SkipsMessageSet(t *testing.T) {
	t.Parallel()

	// Mirrors the real-world shape this was found against
	// (buf.build/svanburen/protobuf-conformance's
	// TestAllTypesProto2.MessageSetCorrect): one message declares
	// "option message_set_wire_format = true;" (the legacy proto1 MessageSet
	// wire format, which protodesc.NewFiles refuses to build at all), a
	// second message declares an "extend" block against it, and a third,
	// unrelated message has nothing to do with either.
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("mset.proto"),
		Syntax:  new("proto2"),
		Package: new("mset"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name:    new("Legacy"),
				Options: &descriptorpb.MessageOptions{MessageSetWireFormat: new(true)},
				ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
					{Start: new(int32(4)), End: new(int32(536870912))},
				},
			},
			{
				Name: new("LegacyExtension"),
				Extension: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     new("legacy_extension"),
						Number:   new(int32(1000)),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".mset.LegacyExtension"),
						Extendee: new(".mset.Legacy"),
					},
				},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: new("str"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				},
			},
			{
				Name: new("Fine"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: new("ok"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				},
			},
		},
	}

	fdsBytes, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}})
	attest.Ok(t, err, attest.Fatal())

	// Confirm the premise: a naive build fails outright on the MessageSet
	// message, taking every other type in the file down with it.
	var plainFDS descriptorpb.FileDescriptorSet
	attest.Ok(t, proto.Unmarshal(fdsBytes, &plainFDS), attest.Fatal())
	_, err = protodesc.NewFiles(&plainFDS)
	attest.True(t, err != nil, attest.Sprintf("expected the naive build to fail on the MessageSet message"))

	// resolveRegistry should skip just the MessageSet message and the
	// now-dangling extension declaration against it, and still build
	// everything else in the file -- including LegacyExtension itself,
	// which is a perfectly ordinary message that merely also happened to
	// declare that extend block.
	regFiles, err := resolveRegistry(fdsBytes)
	attest.Ok(t, err, attest.Fatal())

	_, err = regFiles.FindDescriptorByName("mset.Legacy")
	attest.True(t, err != nil, attest.Sprintf("the MessageSet message itself should have been skipped"))

	legacyExt, err := regFiles.FindDescriptorByName("mset.LegacyExtension")
	attest.Ok(t, err, attest.Fatal())
	attest.Equal(t, string(legacyExt.(protoreflect.MessageDescriptor).Fields().Get(0).Name()), "str")

	fine, err := regFiles.FindDescriptorByName("mset.Fine")
	attest.Ok(t, err, attest.Fatal())
	attest.Equal(t, string(fine.(protoreflect.MessageDescriptor).Fields().Get(0).Name()), "ok")
}

func TestResolveRegistry_NoCustomOptions(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("plain.proto"),
		Syntax:  new("proto3"),
		Package: new("plain"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("x"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
			},
		}},
	}
	fdsBytes, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}})
	attest.Ok(t, err, attest.Fatal())

	regFiles, err := resolveRegistry(fdsBytes)
	attest.Ok(t, err, attest.Fatal())
	m, err := regFiles.FindDescriptorByName("plain.M")
	attest.Ok(t, err, attest.Fatal())
	attest.Equal(t, string(m.(protoreflect.MessageDescriptor).Fields().Get(0).Name()), "x")
}
