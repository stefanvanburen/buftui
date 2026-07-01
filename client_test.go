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
		Name:       ptr("custom.proto"),
		Syntax:     ptr("proto3"),
		Package:    ptr("custom"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		Extension: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     ptr("my_label"),
				Number:   ptr(int32(50001)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Extendee: ptr(".google.protobuf.FieldOptions"),
			},
		},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("labeled"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
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

func TestResolveRegistry_NoCustomOptions(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    ptr("plain.proto"),
		Syntax:  ptr("proto3"),
		Package: ptr("plain"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: ptr("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("x"), Number: ptr(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
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
