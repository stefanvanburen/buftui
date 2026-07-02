package main

import (
	"strings"
	"testing"

	"go.akshayshah.org/attest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// This file models, in miniature, the real-world structures found live
// against buf.build/svanburen/protobuf-conformance (the official protobuf
// conformance test suite) during a manual dogfooding session. Each of these
// combines multiple features in one message/file the way real schemas do,
// rather than testing one feature in isolation like the rest of
// docs_test.go -- the bugs found that day were all about interactions
// (a MessageSet message coexisting with an ordinary one that extends it, an
// Editions field's encoding depending on both a file-level default and a
// field-level override, ...) that single-feature unit tests can't catch.
// Keeping this scenario committed means a future regression shows up in
// `go test` instead of requiring another live BSR session to rediscover.

// TestConformanceFixture_Proto2Legacy mirrors TestAllTypesProto2's
// MessageSetCorrect/MessageSetCorrectExtension1 pattern: a message using the
// legacy MessageSet wire format, a second message that both extends it and
// has its own ordinary field, and an unrelated third message -- verifying
// that skipping the MessageSet message doesn't take anything else down with
// it, and that proto2's "optional"/"required"/group rendering all still work
// alongside it.
func TestConformanceFixture_Proto2Legacy(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("fixture_proto2.proto"),
		Syntax:  new("proto2"),
		Package: new("fixture.proto2"),
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
						TypeName: new(".fixture.proto2.LegacyExtension"),
						Extendee: new(".fixture.proto2.Legacy"),
					},
				},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: new("str"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
				},
			},
			{
				Name: new("WithGroup"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: new("plain_optional"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					{Name: new("must_have"), Number: new(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_REQUIRED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
					{Name: new("data"), Number: new(int32(3)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_GROUP.Enum(), TypeName: new(".fixture.proto2.WithGroup.Data")},
				},
				NestedType: []*descriptorpb.DescriptorProto{{
					Name: new("Data"),
					Field: []*descriptorpb.FieldDescriptorProto{
						{Name: new("group_int32"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
					},
				}},
				ReservedRange: []*descriptorpb.DescriptorProto_ReservedRange{
					{Start: new(int32(100)), End: new(int32(200))},
				},
				ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{
					{Start: new(int32(300)), End: new(int32(400))},
				},
			},
		},
	}

	// Route through resolveRegistry (not the simpler buildTestRegistry helper
	// used elsewhere in this package) since MessageSet-skipping is
	// resolveRegistry's job specifically -- protodesc.NewFiles on its own
	// still refuses the whole set outright.
	fdsBytes, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}})
	attest.Ok(t, err, attest.Fatal())
	files, err := resolveRegistry(fdsBytes)
	attest.Ok(t, err, attest.Fatal())
	items := packagesFromDocs(files, map[string]bool{"fixture_proto2.proto": true})
	attest.Equal(t, len(items), 1, attest.Fatal())
	out := renderPackage(items[0].(*docsPackage), false)

	// The MessageSet message itself is gone...
	attest.False(t, strings.Contains(out, "message_set"), attest.Sprintf("MessageSet leaked into output: %q", out))
	// ...but the message that merely extended it survives, with its own
	// field intact, and no dangling extend block for the removed type.
	attest.True(t, strings.Contains(out, "LegacyExtension"), attest.Sprintf("sibling message wrongly removed: %q", out))
	attest.True(t, strings.Contains(out, "str = 1"), attest.Sprintf("sibling message's own field missing: %q", out))
	// proto2 cardinality keywords, all in the same message.
	attest.True(t, strings.Contains(out, "optional string"), attest.Sprintf("optional keyword missing: %q", out))
	attest.True(t, strings.Contains(out, "required int32"), attest.Sprintf("required keyword missing: %q", out))
	attest.True(t, strings.Contains(out, "optional group Data"), attest.Sprintf("group keyword missing: %q", out))
	// Reserved and extension ranges on the same message as the group field.
	attest.True(t, strings.Contains(out, "reserved 100 to 199;"), attest.Sprintf("reserved range missing: %q", out))
	attest.True(t, strings.Contains(out, "extensions 300 to 399;"), attest.Sprintf("extension range missing: %q", out))
}

// TestConformanceFixture_EditionsWireEncoding mirrors
// TestAllTypesEdition2023's file-level "features.message_encoding =
// DELIMITED" default combined with a per-field override back to
// LENGTH_PREFIXED, and a repeated field with an explicit
// repeated_field_encoding override -- verifying that only genuine
// deviations from an inherited default are annotated, not the default
// itself.
func TestConformanceFixture_EditionsWireEncoding(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("fixture_editions.proto"),
		Syntax:  new("editions"),
		Edition: descriptorpb.Edition_EDITION_2023.Enum(),
		Package: new("fixture.editions"),
		Options: &descriptorpb.FileOptions{
			Features: &descriptorpb.FeatureSet{MessageEncoding: descriptorpb.FeatureSet_DELIMITED.Enum()},
		},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: new("Container"),
				Field: []*descriptorpb.FieldDescriptorProto{
					// Inherits the file's DELIMITED default with no
					// per-field override -- Kind() is GroupKind, but since
					// this is Editions there's no "group" keyword to show,
					// and no per-field annotation either (matches the file
					// header, nothing to call out).
					{
						Name: new("inherited"), Number: new(int32(1)),
						Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".fixture.editions.GroupLike"),
					},
					// Explicitly opts back out to LENGTH_PREFIXED.
					{
						Name: new("opted_out"), Number: new(int32(2)),
						Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".fixture.editions.GroupLike"),
						Options: &descriptorpb.FieldOptions{
							Features: &descriptorpb.FeatureSet{MessageEncoding: descriptorpb.FeatureSet_LENGTH_PREFIXED.Enum()},
						},
					},
					// Explicit repeated_field_encoding override, same message.
					{
						Name: new("expanded"), Number: new(int32(3)),
						Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
						Options: &descriptorpb.FieldOptions{
							Features: &descriptorpb.FeatureSet{RepeatedFieldEncoding: descriptorpb.FeatureSet_EXPANDED.Enum()},
						},
					},
				},
			},
			{
				Name: new("GroupLike"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: new("a"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
				},
			},
		},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"fixture_editions.proto": true})
	attest.Equal(t, len(items), 1, attest.Fatal())
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, `[features.message_encoding = DELIMITED]`), attest.Sprintf("file-level default missing: %q", out))
	attest.False(t, strings.Contains(out, "group GroupLike"), attest.Sprintf("Editions has no group keyword: %q", out))
	attest.True(t, strings.Contains(out, "inherited = 1"), attest.Sprintf("field inheriting the file default missing: %q", out))
	// Only the two fields with an explicit per-field override should carry a
	// features.* annotation -- the one that just inherits the file default
	// shouldn't (verified by count: exactly 2 field-level occurrences, plus
	// the 1 file-level "message_encoding" already asserted above).
	attest.Equal(t, strings.Count(out, "message_encoding"), 2, attest.Sprintf("expected exactly one file-level and one field-level message_encoding mention: %q", out))
	attest.True(t, strings.Contains(out, "opted_out = 2"), attest.Sprintf("field with an explicit override missing: %q", out))
	attest.True(t, strings.Contains(out, "[features.message_encoding = LENGTH_PREFIXED]"), attest.Sprintf("explicit per-field override missing: %q", out))
	attest.True(t, strings.Contains(out, "expanded = 3"), attest.Sprintf("field with an explicit repeated_field_encoding override missing: %q", out))
	attest.True(t, strings.Contains(out, "[features.repeated_field_encoding = EXPANDED]"), attest.Sprintf("explicit repeated_field_encoding override missing: %q", out))
}

// TestConformanceFixture_Proto3Combined mirrors TestAllTypesProto3: a oneof,
// an allow_alias enum with multiple aliases of the same value, an explicit
// packed repeated field, and field names deliberately shaped to stress the
// snake_case-to-camelCase JSON name derivation (leading/trailing/double
// underscores, digits, mixed case) -- verifying none of those false-positive
// a json_name override.
func TestConformanceFixture_Proto3Combined(t *testing.T) {
	t.Parallel()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    new("fixture_proto3.proto"),
		Syntax:  new("proto3"),
		Package: new("fixture.proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: new("packed_int32"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Options: &descriptorpb.FieldOptions{Packed: new(true)}},
				{Name: new("oneof_string"), Number: new(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), OneofIndex: new(int32(0))},
				{Name: new("oneof_int32"), Number: new(int32(3)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), OneofIndex: new(int32(0))},
				{Name: new("fieldname1"), Number: new(int32(4)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
				{Name: new("_field_name2"), Number: new(int32(5)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
				{Name: new("field__name3_"), Number: new(int32(6)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
				{Name: new("FIELD_NAME4"), Number: new(int32(7)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
			},
			OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: new("choice")}},
		}},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name:    new("AliasedEnum"),
			Options: &descriptorpb.EnumOptions{AllowAlias: new(true)},
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: new("ALIAS_FOO"), Number: new(int32(0))},
				{Name: new("ALIAS_BAR"), Number: new(int32(1))},
				{Name: new("MOO"), Number: new(int32(1))},
				{Name: new("moo"), Number: new(int32(1))},
			},
		}},
	}

	files := buildTestRegistry(t, fdp)
	items := packagesFromDocs(files, map[string]bool{"fixture_proto3.proto": true})
	attest.Equal(t, len(items), 1, attest.Fatal())
	out := renderPackage(items[0].(*docsPackage), false)

	attest.True(t, strings.Contains(out, "[packed = true]"), attest.Sprintf("packed annotation missing: %q", out))
	attest.True(t, strings.Contains(out, "oneof choice {"), attest.Sprintf("oneof block missing: %q", out))
	attest.True(t, strings.Contains(out, "MOO = 1"), attest.Sprintf("first alias missing: %q", out))
	attest.True(t, strings.Contains(out, "moo = 1"), attest.Sprintf("second alias missing: %q", out))
	attest.Equal(t, strings.Count(out, "[alias of ALIAS_BAR]"), 2, attest.Sprintf("expected both MOO and moo to resolve to the same canonical alias: %q", out))
	// None of the unusually-shaped field names should trip a false-positive
	// json_name annotation.
	attest.False(t, strings.Contains(out, "json_name"), attest.Sprintf("unexpected json_name annotation: %q", out))
}
