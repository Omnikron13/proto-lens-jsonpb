// This code is heavily inspired by:
// https://github.com/twitchtv/twirp-ruby/blob/master/protoc-gen-twirp_ruby/main.go
// which is licensed under the Apache License, Version 2.0.

package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/protoc-gen-go/descriptor"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
	"github.com/twitchtv/protogen/typemap"
)

func main() {
	genReq := readGenRequest(os.Stdin)
	g := &generator{version: Version, genReq: genReq}
	genResp := g.Generate()
	writeGenResponse(os.Stdout, genResp)
}

type generator struct {
	version string
	genReq  *plugin.CodeGeneratorRequest
	reg     *typemap.Registry
}

func (g *generator) Generate() *plugin.CodeGeneratorResponse {
	resp := new(plugin.CodeGeneratorResponse)

	for _, f := range g.protoFilesToGenerate() {
		jsonpbFileName := "Proto/" + packageFileName(filePath(f)) + "_JSON.hs"

		haskellCode := g.generateHaskellCode(f)
		respFile := &plugin.CodeGeneratorResponse_File{
			Name:    proto.String(jsonpbFileName),
			Content: proto.String(haskellCode),
		}
		resp.File = append(resp.File, respFile)
	}

	return resp
}

func (g *generator) generateHaskellCode(file *descriptor.FileDescriptorProto) string {
	b := new(bytes.Buffer)
	print(b, "-- Code generated by protoc-gen-jsonpb_haskell %s, DO NOT EDIT.", g.version)
	// print(b, "{-# LANGUAGE DerivingVia, DeriveAnyClass, DuplicateRecordFields #-}")
	print(b, "{-# LANGUAGE OverloadedStrings #-}")
	print(b, "{-# OPTIONS_GHC -Wno-orphans -Wno-unused-imports -Wno-missing-export-lists #-}")

	moduleName := packageFileName(filePath(file))
	print(b, "module Proto.%s_JSON where", moduleName)
	print(b, "")

	print(b, "import           Prelude(($), (.), (<$>), pure, show, Maybe(..))")
	print(b, "")

	print(b, "import           Data.ProtoLens.Runtime.Lens.Family2 ((^.), (.~), (&))")
	print(b, "import           Data.Monoid (mconcat)")
	print(b, "import           Control.Monad (msum)")
	print(b, "import           Data.ProtoLens (defMessage)")
	print(b, "import qualified Data.Aeson as A")
	print(b, "import qualified Data.Aeson.Encoding as E")
	print(b, "import           Data.ProtoLens.JSONPB as JSONPB")
	print(b, "import qualified Data.Text as T")
	print(b, "")

	print(b, "import           Proto.%s as P", moduleName)
	print(b, "import           Proto.%s_Fields as P", moduleName)

	for _, message := range file.MessageType {
		generateMessage(b, message)
	}
	for _, enum := range file.EnumType {
		generateEnum(b, enum, nil)
	}

	return b.String()
}

func constructorFor(message *descriptor.DescriptorProto) string {
	ctor := message.GetName()

	if len(message.Field) > 0 {
		ctor += "{..}"
	}

	return ctor
}

func generateMessage(b *bytes.Buffer, message *descriptor.DescriptorProto) {
	oneofs := []string{}
	for _, oneof := range message.OneofDecl {
		generateOneof(b, message, oneof)
		oneofs = append(oneofs, oneof.GetName())
	}

	n := message.GetName()
	noFields := len(message.Field) == 0

	// Generate a FromJSONPB Instance
	// Empty datatypes require an invocation of `pure`
	print(b, "")
	print(b, "instance FromJSONPB %s where", n)

	if noFields {
		print(b, "  parseJSONPB = withObject \"%s\" $ \\_ -> pure defMessage", n)
	} else {
		print(b, "  parseJSONPB = withObject \"%s\" $ \\obj -> do", n)
		for _, f := range fieldsForMessageInstance(message, "<$>", "<*>") {
			if f.isMaybeField {
				print(b, "    %s' <- obj A..:? \"%s\"", f.fieldName, f.fieldName)
			} else {
				print(b, "    %s' <- obj .: \"%s\"", f.fieldName, f.fieldName)
			}
		}
		print(b, "    pure $ defMessage")
		for _, f := range fieldsForMessageInstance(message, "<$>", "<*>") {
			print(b, "      & P.%s .~ %s'", f.lensFieldName, f.fieldName)
		}
	}

	// Generate a ToJSONPB Instance
	print(b, "")
	print(b, "instance ToJSONPB %s where", n)
	if noFields {
		print(b, "  toJSONPB _ = object []")
	} else {
		print(b, "  toJSONPB x = object")
		for _, f := range fieldsForMessageInstance(message, "[", ",") {
			// if f.isMaybeField {
			// 	print(b, "    %s \"%s\" .= (x^.%s)", f.sep, f.fieldName, f.fieldName)
			// } else {
			// }
			print(b, "    %s \"%s\" .= (x^.%s)", f.sep, f.fieldName, f.lensFieldName)
		}
		print(b, "    ]")
	}
	if noFields {
		print(b, "  toEncodingPB _ = pairs []")
	} else {
		print(b, "  toEncodingPB x = pairs")
		for _, f := range fieldsForMessageInstance(message, "[", ",") {
			print(b, "    %s \"%s\" .= (x^.%s)", f.sep, f.fieldName, f.lensFieldName)
		}
		print(b, "    ]")
	}

	printToFromJSONInstances(b, n)

	for _, nested := range message.NestedType {
		generateMessage(b, nested)
	}
	for _, enum := range message.EnumType {
		generateEnum(b, enum, message)
	}
}

func generateOneof(b *bytes.Buffer, message *descriptor.DescriptorProto, oneof *descriptor.OneofDescriptorProto) {
	outerName := strings.Title(message.GetName())
	oneofName := oneof.GetName()
	n := fmt.Sprintf("%s'%s", outerName, pascalCase(oneofName))

	// Generate a FromJSONPB Instance
	print(b, "")
	print(b, "instance FromJSONPB %s where", n)
	print(b, "  parseJSONPB = A.withObject \"%s\" $ \\obj -> mconcat", n)
	print(b, "    [")
	for _, f := range fieldsForOneOfInstance(message, " ", ",") {
		print(b, "    %s %s'%s <$> parseField obj \"%s\"", f.sep, outerName, f.fieldName, f.rawFieldName)
	}
	print(b, "    ]")

	// Generate a ToJSONPB Instance
	print(b, "")
	print(b, "instance ToJSONPB %s where", n)
	for _, f := range fieldsForOneOfInstance(message, " ", ",") {
		x := "x"
		if f.isMaybeField {
			x = "Just x"
		}
		print(b, "  toJSONPB (%s'%s x) = object [ \"%s\" .= %s ]", outerName, f.fieldName, f.rawFieldName, x)
	}

	for _, f := range fieldsForOneOfInstance(message, " ", ",") {
		x := "x"
		if f.isMaybeField {
			x = "Just x"
		}
		print(b, "  toEncodingPB (%s'%s x) = pairs [ \"%s\" .= %s ]", outerName, f.fieldName, f.rawFieldName, x)
	}

	printToFromJSONInstances(b, n)
}

func generateEnum(b *bytes.Buffer, enum *descriptor.EnumDescriptorProto, parent *descriptor.DescriptorProto) {
	n := enum.GetName()
	qualifiedName := n
	if parent != nil {
		qualifiedName = fmt.Sprintf("%s'%s", parent.GetName(), n)
	}

	// Generate a FromJSONPB Instance
	print(b, "")
	print(b, "instance FromJSONPB %s where", qualifiedName)
	for _, value := range enum.Value {
		enum := strings.ToUpper(value.GetName())
		v := strings.ToUpper(value.GetName())
		if parent != nil {
			v = fmt.Sprintf("%s'%s", parent.GetName(), v)
		}
		print(b, "  parseJSONPB (JSONPB.String \"%s\") = pure %s", enum, v)
	}
	print(b, "  parseJSONPB x = typeMismatch \"%s\" x", n)

	// Generate a ToJSONPB Instance
	print(b, "")
	print(b, "instance ToJSONPB %s where", qualifiedName)
	print(b, "  toJSONPB x _ = A.String . T.toUpper . T.pack $ show x")
	print(b, "  toEncodingPB x _ = E.text . T.toUpper . T.pack  $ show x")

	printToFromJSONInstances(b, qualifiedName)
}

type aField struct {
	sep           string
	lensFieldName string
	fieldName     string
	isMaybeField  bool
	rawFieldName  string
}

func fieldsForOneOfInstance(message *descriptor.DescriptorProto, firstSep string, restSep string) []aField {
	fields := []aField{}
	first := true
	for _, field := range message.Field {
		fieldName := pascalCase(field.GetName())
		if field.OneofIndex != nil {
			sep := restSep
			if first {
				sep = firstSep
			}
			isMaybeField := false
			switch *field.Type {
			case descriptor.FieldDescriptorProto_TYPE_MESSAGE:
				if field.GetLabel() != descriptor.FieldDescriptorProto_LABEL_REPEATED {
					isMaybeField = true
				}
			}
			fields = append(fields, aField{sep: sep, fieldName: fieldName, isMaybeField: isMaybeField, rawFieldName: field.GetName()})
			first = false
		}
	}
	return fields
}

func fieldsForMessageInstance(message *descriptor.DescriptorProto, firstSep string, restSep string) []aField {
	fields := []aField{}

	first := true
	for _, field := range message.Field {
		if field.OneofIndex == nil {
			fieldName := toHaskellFieldName(field.GetName())
			sep := restSep
			if first {
				sep = firstSep
			}
			lensFieldName := fieldName
			isMaybeField := false
			switch *field.Type {
			// case descriptor.FieldDescriptorProto_TYPE_ENUM:
			case descriptor.FieldDescriptorProto_TYPE_MESSAGE:
				if field.GetLabel() != descriptor.FieldDescriptorProto_LABEL_REPEATED {
					lensFieldName = "maybe'" + fieldName
					isMaybeField = true
				}
			}

			fields = append(fields, aField{sep: sep, fieldName: fieldName, isMaybeField: isMaybeField, lensFieldName: lensFieldName})
			first = false
		}
	}
	for _, oneof := range message.OneofDecl {
		fieldName := toHaskellFieldName(oneof.GetName())
		sep := restSep
		if first {
			sep = firstSep
		}
		fields = append(fields, aField{sep: sep, fieldName: fieldName, isMaybeField: true, lensFieldName: "maybe'" + fieldName})
		first = false
	}
	return fields
}

func printToFromJSONInstances(b *bytes.Buffer, n string) {
	print(b, "")
	print(b, "instance FromJSON %s where", n)
	print(b, "  parseJSON = parseJSONPB")

	print(b, "")
	print(b, "instance ToJSON %s where", n)
	print(b, "  toJSON = toAesonValue")
	print(b, "  toEncoding = toAesonEncoding")
}

// Reference: https://github.com/golang/protobuf/blob/c823c79ea1570fb5ff454033735a8e68575d1d0f/protoc-gen-go/descriptor/descriptor.proto#L136
func toType(field *descriptor.FieldDescriptorProto, prefix string, suffix string) string {
	label := field.GetLabel()
	res := ""
	switch *field.Type {
	case descriptor.FieldDescriptorProto_TYPE_INT32:
		res = "Int32"
	case descriptor.FieldDescriptorProto_TYPE_INT64:
		res = "Int64"
	case descriptor.FieldDescriptorProto_TYPE_SINT32:
		res = "Int32"
	case descriptor.FieldDescriptorProto_TYPE_SINT64:
		res = "Int64"
	case descriptor.FieldDescriptorProto_TYPE_SFIXED32:
		res = fmt.Sprintf("%sSigned Int32%s", prefix, suffix)
	case descriptor.FieldDescriptorProto_TYPE_SFIXED64:
		res = fmt.Sprintf("%sSigned Int64%s", prefix, suffix)
	case descriptor.FieldDescriptorProto_TYPE_UINT32:
		res = "Word32"
	case descriptor.FieldDescriptorProto_TYPE_UINT64:
		res = "Word64"
	case descriptor.FieldDescriptorProto_TYPE_FIXED32:
		res = fmt.Sprintf("%sFixed Word32%s", prefix, suffix)
	case descriptor.FieldDescriptorProto_TYPE_FIXED64:
		res = fmt.Sprintf("%sFixed Word64%s", prefix, suffix)
	case descriptor.FieldDescriptorProto_TYPE_STRING:
		res = "Text"
	case descriptor.FieldDescriptorProto_TYPE_BYTES:
		res = "ByteString"
	case descriptor.FieldDescriptorProto_TYPE_BOOL:
		res = "Bool"
	case descriptor.FieldDescriptorProto_TYPE_FLOAT:
		res = "Float"
	case descriptor.FieldDescriptorProto_TYPE_DOUBLE:
		res = "Double"
	case descriptor.FieldDescriptorProto_TYPE_MESSAGE:
		res = toHaskellType(field.GetTypeName())
	case descriptor.FieldDescriptorProto_TYPE_ENUM:
		res = toHaskellType(field.GetTypeName())
	default:
		Fail(fmt.Sprintf("no mapping for type %s", field.GetType()))
	}

	if label == descriptor.FieldDescriptorProto_LABEL_REPEATED {
		if *field.Type == descriptor.FieldDescriptorProto_TYPE_MESSAGE {
			res = fmt.Sprintf("Vector %s", res)
		} else {
			res = fmt.Sprintf("Vector %s", res)
		}
	} else if *field.Type == descriptor.FieldDescriptorProto_TYPE_MESSAGE {
		res = fmt.Sprintf("%sMaybe %s%s", prefix, res, suffix)
	}

	return res
}

// .foo.Message => Message
// google.protobuf.Empty => Google.Protobuf.Empty
func toHaskellType(s string) string {
	if len(s) > 1 && s[0:1] == "." {
		parts := strings.Split(s, ".")
		return parts[len(parts)-1]
	}

	parts := []string{}
	for _, x := range strings.Split(s, ".") {
		parts = append(parts, strings.Title(x))
	}
	return strings.Join(parts, ".")
}

// handle some names that are hard to deal with in Haskell like `id`.
func toHaskellFriendlyName(s string) string {
	switch s {
	case "type":
		return s + "'"
	default:
		return s
	}
}

// snake_case to camelCase.
func toHaskellFieldName(s string) string {
	parts := []string{}
	for i, x := range strings.Split(s, "_") {
		if i == 0 {
			parts = append(parts, strings.ToLower(x))
		} else {
			parts = append(parts, strings.Title(strings.ToLower(x)))
		}
	}
	return toHaskellFriendlyName(strings.Join(parts, ""))
}

// protoFilesToGenerate selects descriptor proto files that were explicitly listed on the command-line.
func (g *generator) protoFilesToGenerate() []*descriptor.FileDescriptorProto {
	files := []*descriptor.FileDescriptorProto{}
	for _, name := range g.genReq.FileToGenerate { // explicitly listed on the command-line
		for _, f := range g.genReq.ProtoFile { // all files and everything they import
			if f.GetName() == name { // match
				files = append(files, f)
				continue
			}
		}
	}
	return files
}

func print(buf *bytes.Buffer, tpl string, args ...interface{}) {
	buf.WriteString(fmt.Sprintf(tpl, args...))
	buf.WriteByte('\n')
}

func filePath(f *descriptor.FileDescriptorProto) string {
	return *f.Name
}

// capitalize, with exceptions for common abbreviations
func capitalize(s string) string {
	return strings.Title(strings.ToLower(s))
}

func camelCase(s string) string {
	parts := []string{}
	for i, x := range strings.Split(s, "_") {
		if i == 0 {
			parts = append(parts, strings.ToLower(x))
		} else {
			parts = append(parts, capitalize(x))
		}
	}
	return strings.Join(parts, "")
}

func pascalCase(s string) string {
	parts := []string{}
	for _, x := range strings.Split(s, "_") {
		parts = append(parts, capitalize(x))
	}
	return strings.Join(parts, "")
}

func packageFileName(path string) string {
	ext := filepath.Ext(path)
	return pascalCase(strings.TrimSuffix(path, ext))
}

func packageType(path string) string {
	ext := filepath.Ext(path)
	path = strings.TrimSuffix(filepath.Base(path), ext)
	return pascalCase(path)
}

func toModuleName(file *descriptor.FileDescriptorProto) string {
	pkgName := file.GetPackage()

	parts := []string{}
	for _, p := range strings.Split(pkgName, ".") {
		parts = append(parts, capitalize(p))
	}

	apiName := packageType(filePath(file))
	parts = append(parts, apiName)

	return strings.Join(parts, ".")
}

func Fail(msgs ...string) {
	s := strings.Join(msgs, " ")
	log.Print("error:", s)
	os.Exit(1)
}

func readGenRequest(r io.Reader) *plugin.CodeGeneratorRequest {
	data, err := ioutil.ReadAll(r)
	if err != nil {
		Fail(err.Error(), "reading input")
	}

	req := new(plugin.CodeGeneratorRequest)
	if err = proto.Unmarshal(data, req); err != nil {
		Fail(err.Error(), "parsing input proto")
	}

	if len(req.FileToGenerate) == 0 {
		Fail("no files to generate")
	}

	return req
}

func writeGenResponse(w io.Writer, resp *plugin.CodeGeneratorResponse) {
	data, err := proto.Marshal(resp)
	if err != nil {
		Fail(err.Error(), "marshaling response")
	}
	_, err = w.Write(data)
	if err != nil {
		Fail(err.Error(), "writing response")
	}
}
