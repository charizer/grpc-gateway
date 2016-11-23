package genswagger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	pbdescriptor "github.com/golang/protobuf/protoc-gen-go/descriptor"
	"github.com/grpc-ecosystem/grpc-gateway/protoc-gen-grpc-gateway/descriptor"
)

// findServicesMessagesAndEnumerations discovers all messages and enums defined in the RPC methods of the service.
func findServicesMessagesAndEnumerations(s []*descriptor.Service, reg *descriptor.Registry, m messageMap, e enumMap) {
	for _, svc := range s {
		for _, meth := range svc.Methods {
			m[fullyQualifiedNameToSwaggerName(meth.RequestType.FQMN(), reg)] = meth.RequestType
			findNestedMessagesAndEnumerations(meth.RequestType, reg, m, e)
			m[fullyQualifiedNameToSwaggerName(meth.ResponseType.FQMN(), reg)] = meth.ResponseType
			findNestedMessagesAndEnumerations(meth.ResponseType, reg, m, e)
		}
	}
}

// findNestedMessagesAndEnumerations those can be generated by the services.
func findNestedMessagesAndEnumerations(message *descriptor.Message, reg *descriptor.Registry, m messageMap, e enumMap) {
	// Iterate over all the fields that
	for _, t := range message.Fields {
		fieldType := t.GetTypeName()
		// If the type is an empty string then it is a proto primitive
		if fieldType != "" {
			if _, ok := m[fieldType]; !ok {
				msg, err := reg.LookupMsg("", fieldType)
				if err != nil {
					enum, err := reg.LookupEnum("", fieldType)
					if err != nil {
						panic(err)
					}
					e[fieldType] = enum
					continue
				}
				m[fieldType] = msg
				findNestedMessagesAndEnumerations(msg, reg, m, e)
			}
		}
	}
}

func renderMessagesAsDefinition(messages messageMap, d swaggerDefinitionsObject, reg *descriptor.Registry) {
	for name, msg := range messages {
		switch name {
		case ".google.protobuf.Timestamp":
			continue
		}
		if opt := msg.GetOptions(); opt != nil && opt.MapEntry != nil && *opt.MapEntry {
			continue
		}
		schema := swaggerSchemaObject{
			schemaCore: schemaCore{
				Type: "object",
			},
		}
		msgComments := protoComments(reg, msg.File, msg.Outers, "MessageType", int32(msg.Index))
		if err := updateSwaggerDataFromComments(&schema, msgComments); err != nil {
			panic(err)
		}

		for i, f := range msg.Fields {
			fieldValue := schemaOfField(f, reg)

			fieldProtoPath := protoPathIndex(reflect.TypeOf((*pbdescriptor.DescriptorProto)(nil)), "Field")
			fieldProtoComments := protoComments(reg, msg.File, msg.Outers, "MessageType", int32(msg.Index), fieldProtoPath, int32(i))
			if err := updateSwaggerDataFromComments(&fieldValue, fieldProtoComments); err != nil {
				panic(err)
			}

			schema.Properties = append(schema.Properties, keyVal{f.GetName(), fieldValue})
		}
		d[fullyQualifiedNameToSwaggerName(msg.FQMN(), reg)] = schema
	}
}

// schemaOfField returns a swagger Schema Object for a protobuf field.
func schemaOfField(f *descriptor.Field, reg *descriptor.Registry) swaggerSchemaObject {
	const (
		singular = 0
		array    = 1
		object   = 2
	)
	var (
		core      schemaCore
		aggregate int
	)

	fd := f.FieldDescriptorProto
	if m, err := reg.LookupMsg("", f.GetTypeName()); err == nil {
		if opt := m.GetOptions(); opt != nil && opt.MapEntry != nil && *opt.MapEntry {
			fd = m.GetField()[1]
			aggregate = object
		}
	}
	if fd.GetLabel() == pbdescriptor.FieldDescriptorProto_LABEL_REPEATED {
		aggregate = array
	}

	switch ft := fd.GetType(); ft {
	case pbdescriptor.FieldDescriptorProto_TYPE_ENUM, pbdescriptor.FieldDescriptorProto_TYPE_MESSAGE, pbdescriptor.FieldDescriptorProto_TYPE_GROUP:
		if fd.GetTypeName() == ".google.protobuf.Timestamp" && pbdescriptor.FieldDescriptorProto_TYPE_MESSAGE == ft {
			core = schemaCore{
				Type:   "string",
				Format: "date-time",
			}
		} else {
			core = schemaCore{
				Ref: "#/definitions/" + fullyQualifiedNameToSwaggerName(fd.GetTypeName(), reg),
			}
		}
	default:
		ftype, format, ok := primitiveSchema(ft)
		if ok {
			core = schemaCore{Type: ftype, Format: format}
		} else {
			core = schemaCore{Type: ft.String(), Format: "UNKNOWN"}
		}
	}
	switch aggregate {
	case array:
		return swaggerSchemaObject{
			schemaCore: schemaCore{
				Type: "array",
			},
			Items: (*swaggerItemsObject)(&core),
		}
	case object:
		return swaggerSchemaObject{
			schemaCore: schemaCore{
				Type: "object",
			},
			AdditionalProperties: &swaggerSchemaObject{schemaCore: core},
		}
	default:
		return swaggerSchemaObject{schemaCore: core}
	}
}

// primitiveSchema returns a pair of "Type" and "Format" in JSON Schema for
// the given primitive field type.
// The last return parameter is true iff the field type is actually primitive.
func primitiveSchema(t pbdescriptor.FieldDescriptorProto_Type) (ftype, format string, ok bool) {
	switch t {
	case pbdescriptor.FieldDescriptorProto_TYPE_DOUBLE:
		return "number", "double", true
	case pbdescriptor.FieldDescriptorProto_TYPE_FLOAT:
		return "number", "float", true
	case pbdescriptor.FieldDescriptorProto_TYPE_INT64:
		return "string", "int64", true
	case pbdescriptor.FieldDescriptorProto_TYPE_UINT64:
		// 64bit integer types are marshaled as string in the default JSONPb marshaler.
		// TODO(yugui) Add an option to declare 64bit integers as int64.
		//
		// NOTE: uint64 is not a predefined format of integer type in Swagger spec.
		// So we cannot expect that uint64 is commonly supported by swagger processor.
		return "string", "uint64", true
	case pbdescriptor.FieldDescriptorProto_TYPE_INT32:
		return "integer", "int32", true
	case pbdescriptor.FieldDescriptorProto_TYPE_FIXED64:
		// Ditto.
		return "string", "uint64", true
	case pbdescriptor.FieldDescriptorProto_TYPE_FIXED32:
		// Ditto.
		return "integer", "int64", true
	case pbdescriptor.FieldDescriptorProto_TYPE_BOOL:
		return "boolean", "boolean", true
	case pbdescriptor.FieldDescriptorProto_TYPE_STRING:
		return "string", "string", true
	case pbdescriptor.FieldDescriptorProto_TYPE_BYTES:
		return "string", "byte", true
	case pbdescriptor.FieldDescriptorProto_TYPE_UINT32:
		// Ditto.
		return "integer", "int64", true
	case pbdescriptor.FieldDescriptorProto_TYPE_SFIXED32:
		return "integer", "int32", true
	case pbdescriptor.FieldDescriptorProto_TYPE_SFIXED64:
		return "string", "int64", true
	case pbdescriptor.FieldDescriptorProto_TYPE_SINT32:
		return "integer", "int32", true
	case pbdescriptor.FieldDescriptorProto_TYPE_SINT64:
		return "string", "int64", true
	default:
		return "", "", false
	}
}

// renderEnumerationsAsDefinition inserts enums into the definitions object.
func renderEnumerationsAsDefinition(enums enumMap, d swaggerDefinitionsObject, reg *descriptor.Registry) {
	valueProtoPath := protoPathIndex(reflect.TypeOf((*pbdescriptor.EnumDescriptorProto)(nil)), "Value")
	for _, enum := range enums {
		enumComments := protoComments(reg, enum.File, enum.Outers, "EnumType", int32(enum.Index))

		var enumNames []string
		// it may be necessary to sort the result of the GetValue function.
		var defaultValue string
		var valueDescriptions []string
		for valueIdx, value := range enum.GetValue() {
			enumNames = append(enumNames, value.GetName())
			if defaultValue == "" && value.GetNumber() == 0 {
				defaultValue = value.GetName()
			}

			valueDescription := protoComments(reg, enum.File, enum.Outers, "EnumType", int32(enum.Index), valueProtoPath, int32(valueIdx))
			if valueDescription != "" {
				valueDescriptions = append(valueDescriptions, value.GetName()+": "+valueDescription)
			}
		}

		if len(valueDescriptions) > 0 {
			enumComments += "\n\n - " + strings.Join(valueDescriptions, "\n - ")
		}
		enumSchemaObject := swaggerSchemaObject{
			schemaCore: schemaCore{
				Type: "string",
			},
			Enum:    enumNames,
			Default: defaultValue,
		}
		if err := updateSwaggerDataFromComments(&enumSchemaObject, enumComments); err != nil {
			panic(err)
		}

		d[fullyQualifiedNameToSwaggerName(enum.FQEN(), reg)] = enumSchemaObject
	}
}

// Take in a FQMN or FQEN and return a swagger safe version of the FQMN
func fullyQualifiedNameToSwaggerName(fqn string, reg *descriptor.Registry) string {
	return resolveFullyQualifiedNameToSwaggerName(fqn, append(reg.GetAllFQMNs(), reg.GetAllFQENs()...))
}

// Take the names of every proto and "uniq-ify" them. The idea is to produce a
// set of names that meet a couple of conditions. They must be stable, they
// must be unique, and they must be shorter than the FQN.
//
// This likely could be made better. This will always generate the same names
// but may not always produce optimal names. This is a reasonably close
// approximation of what they should look like in most cases.
func resolveFullyQualifiedNameToSwaggerName(fqn string, messages []string) string {
	packagesByDepth := make(map[int][][]string)
	uniqueNames := make(map[string]string)

	hierarchy := func(pkg string) []string {
		return strings.Split(pkg, ".")
	}

	for _, p := range messages {
		h := hierarchy(p)
		for depth := range h {
			if _, ok := packagesByDepth[depth]; !ok {
				packagesByDepth[depth] = make([][]string, 0)
			}
			packagesByDepth[depth] = append(packagesByDepth[depth], h[len(h)-depth:])
		}
	}

	count := func(list [][]string, item []string) int {
		i := 0
		for _, element := range list {
			if reflect.DeepEqual(element, item) {
				i++
			}
		}
		return i
	}

	for _, p := range messages {
		h := hierarchy(p)
		for depth := 0; depth < len(h); depth++ {
			if count(packagesByDepth[depth], h[len(h)-depth:]) == 1 {
				uniqueNames[p] = strings.Join(h[len(h)-depth-1:], "")
				break
			}
			if depth == len(h)-1 {
				uniqueNames[p] = strings.Join(h, "")
			}
		}
	}
	return uniqueNames[fqn]
}

// Swagger expects paths of the form /path/{string_value} but grpc-gateway paths are expected to be of the form /path/{string_value=strprefix/*}. This should reformat it correctly.
func templateToSwaggerPath(path string) string {
	// It seems like the right thing to do here is to just use
	// strings.Split(path, "/") but that breaks badly when you hit a url like
	// /{my_field=prefix/*}/ and end up with 2 sections representing my_field.
	// Instead do the right thing and write a small pushdown (counter) automata
	// for it.
	var parts []string
	depth := 0
	buffer := ""
	for _, char := range path {
		switch char {
		case '{':
			// Push on the stack
			depth++
			buffer += string(char)
			break
		case '}':
			if depth == 0 {
				panic("Encountered } without matching { before it.")
			}
			// Pop from the stack
			depth--
			buffer += "}"
		case '/':
			if depth == 0 {
				parts = append(parts, buffer)
				buffer = ""
				// Since the stack was empty when we hit the '/' we are done with this
				// section.
				continue
			}
		default:
			buffer += string(char)
			break
		}
	}

	// Now append the last element to parts
	parts = append(parts, buffer)

	// Parts is now an array of segments of the path. Interestingly, since the
	// syntax for this subsection CAN be handled by a regexp since it has no
	// memory.
	re := regexp.MustCompile("{([a-zA-Z][a-zA-Z0-9_.]*).*}")
	for index, part := range parts {
		parts[index] = re.ReplaceAllString(part, "{$1}")
	}

	return strings.Join(parts, "/")
}

func renderServices(services []*descriptor.Service, paths swaggerPathsObject, reg *descriptor.Registry) error {
	// Correctness of svcIdx and methIdx depends on 'services' containing the services in the same order as the 'file.Service' array.
	for svcIdx, svc := range services {
		for methIdx, meth := range svc.Methods {
			for _, b := range meth.Bindings {
				// Iterate over all the swagger parameters
				parameters := swaggerParametersObject{}
				for _, parameter := range b.PathParams {

					var paramType, paramFormat string
					switch pt := parameter.Target.GetType(); pt {
					case pbdescriptor.FieldDescriptorProto_TYPE_GROUP, pbdescriptor.FieldDescriptorProto_TYPE_MESSAGE:
						return fmt.Errorf("only primitive types are allowed in path parameters")
					case pbdescriptor.FieldDescriptorProto_TYPE_ENUM:
						paramType = fullyQualifiedNameToSwaggerName(parameter.Target.GetTypeName(), reg)
						paramFormat = ""
					default:
						var ok bool
						paramType, paramFormat, ok = primitiveSchema(pt)
						if !ok {
							return fmt.Errorf("unknown field type %v", pt)
						}
					}

					parameters = append(parameters, swaggerParameterObject{
						Name:     parameter.String(),
						In:       "path",
						Required: true,
						// Parameters in gRPC-Gateway can only be strings?
						Type:   paramType,
						Format: paramFormat,
					})
				}
				// Now check if there is a body parameter
				if b.Body != nil {
					var schema swaggerSchemaObject

					if len(b.Body.FieldPath) == 0 {
						schema = swaggerSchemaObject{
							schemaCore: schemaCore{
								Ref: fmt.Sprintf("#/definitions/%s", fullyQualifiedNameToSwaggerName(meth.RequestType.FQMN(), reg)),
							},
						}
					} else {
						lastField := b.Body.FieldPath[len(b.Body.FieldPath)-1]
						schema = schemaOfField(lastField.Target, reg)
					}

					desc := ""
					if meth.GetClientStreaming() {
						desc = "(streaming inputs)"
					}
					parameters = append(parameters, swaggerParameterObject{
						Name:        "body",
						Description: desc,
						In:          "body",
						Required:    true,
						Schema:      &schema,
					})
				}

				pathItemObject, ok := paths[templateToSwaggerPath(b.PathTmpl.Template)]
				if !ok {
					pathItemObject = swaggerPathItemObject{}
				}

				methProtoPath := protoPathIndex(reflect.TypeOf((*pbdescriptor.ServiceDescriptorProto)(nil)), "Method")
				desc := ""
				if meth.GetServerStreaming() {
					desc += "(streaming responses)"
				}
				operationObject := &swaggerOperationObject{
					Tags:        []string{svc.GetName()},
					OperationID: fmt.Sprintf("%s", meth.GetName()),
					Parameters:  parameters,
					Responses: swaggerResponsesObject{
						"200": swaggerResponseObject{
							Description: desc,
							Schema: swaggerSchemaObject{
								schemaCore: schemaCore{
									Ref: fmt.Sprintf("#/definitions/%s", fullyQualifiedNameToSwaggerName(meth.ResponseType.FQMN(), reg)),
								},
							},
						},
					},
				}
				methComments := protoComments(reg, svc.File, nil, "Service", int32(svcIdx), methProtoPath, int32(methIdx))
				if err := updateSwaggerDataFromComments(operationObject, methComments); err != nil {
					panic(err)
				}

				switch b.HTTPMethod {
				case "DELETE":
					pathItemObject.Delete = operationObject
					break
				case "GET":
					pathItemObject.Get = operationObject
					break
				case "POST":
					pathItemObject.Post = operationObject
					break
				case "PUT":
					pathItemObject.Put = operationObject
					break
				}
				paths[templateToSwaggerPath(b.PathTmpl.Template)] = pathItemObject
			}
		}
	}

	// Success! return nil on the error object
	return nil
}

// This function is called with a param which contains the entire definition of a method.
func applyTemplate(p param) (string, error) {
	// Create the basic template object. This is the object that everything is
	// defined off of.
	s := swaggerObject{
		// Swagger 2.0 is the version of this document
		Swagger:     "2.0",
		Schemes:     []string{"http", "https"},
		Consumes:    []string{"application/json"},
		Produces:    []string{"application/json"},
		Paths:       make(swaggerPathsObject),
		Definitions: make(swaggerDefinitionsObject),
		Info: swaggerInfoObject{
			Title:   *p.File.Name,
			Version: "version not set",
		},
	}

	// Loops through all the services and their exposed GET/POST/PUT/DELETE definitions
	// and create entries for all of them.
	renderServices(p.Services, s.Paths, p.reg)

	// Find all the service's messages and enumerations that are defined (recursively) and then
	// write their request and response types out as definition objects.
	m := messageMap{}
	e := enumMap{}
	findServicesMessagesAndEnumerations(p.Services, p.reg, m, e)
	renderMessagesAsDefinition(m, s.Definitions, p.reg)
	renderEnumerationsAsDefinition(e, s.Definitions, p.reg)

	// File itself might have some comments and metadata.
	packageProtoPath := protoPathIndex(reflect.TypeOf((*pbdescriptor.FileDescriptorProto)(nil)), "Package")
	packageComments := protoComments(p.reg, p.File, nil, "Package", packageProtoPath)
	if err := updateSwaggerDataFromComments(&s, packageComments); err != nil {
		panic(err)
	}

	// We now have rendered the entire swagger object. Write the bytes out to a
	// string so it can be written to disk.
	var w bytes.Buffer
	enc := json.NewEncoder(&w)
	enc.Encode(&s)

	return w.String(), nil
}

// updateSwaggerDataFromComments updates a Swagger object based on a comment
// from the proto file.
//
// First paragraph of a comment is used for summary. Remaining paragraphs of a
// comment are used for description. If 'Summary' field is not present on the
// passed swaggerObject, the summary and description are joined by \n\n.
//
// If there is a field named 'Info', its 'Summary' and 'Description' fields
// will be updated instead.
//
// If there is no 'Summary', the same behavior will be attempted on 'Title',
// but only if the last character is not a period.
func updateSwaggerDataFromComments(swaggerObject interface{}, comment string) error {
	if len(comment) == 0 {
		return nil
	}

	// Figure out what to apply changes to.
	swaggerObjectValue := reflect.ValueOf(swaggerObject)
	infoObjectValue := swaggerObjectValue.Elem().FieldByName("Info")
	if !infoObjectValue.CanSet() {
		// No such field? Apply summary and description directly to
		// passed object.
		infoObjectValue = swaggerObjectValue.Elem()
	}

	// Figure out which properties to update.
	summaryValue := infoObjectValue.FieldByName("Summary")
	descriptionValue := infoObjectValue.FieldByName("Description")
	usingTitle := false
	if !summaryValue.CanSet() {
		summaryValue = infoObjectValue.FieldByName("Title")
		usingTitle = true
	}

	// If there is a summary (or summary-equivalent), use the first
	// paragraph as summary, and the rest as description.
	if summaryValue.CanSet() {
		paragraphs := strings.Split(comment, "\n\n")

		summary := strings.TrimSpace(paragraphs[0])
		description := strings.TrimSpace(strings.Join(paragraphs[1:], "\n\n"))
		if !usingTitle || summary == "" || summary[len(summary)-1] != '.' {
			if len(summary) > 0 {
				summaryValue.Set(reflect.ValueOf(summary))
			}
			if len(description) > 0 {
				if !descriptionValue.CanSet() {
					return fmt.Errorf("Encountered object type with a summary, but no description")
				}
				descriptionValue.Set(reflect.ValueOf(description))
			}
			return nil
		}
	}

	// There was no summary field on the swaggerObject. Try to apply the
	// whole comment into description.
	if descriptionValue.CanSet() {
		descriptionValue.Set(reflect.ValueOf(comment))
		return nil
	}

	return fmt.Errorf("no description nor summary property")
}

func protoComments(reg *descriptor.Registry, file *descriptor.File, outers []string, typeName string, typeIndex int32, fieldPaths ...int32) string {
	if file.SourceCodeInfo == nil {
		// Curious! A file without any source code info.
		// This could be a test that's providing incomplete
		// descriptor.File information.
		//
		// We could simply return no comments, but panic
		// could make debugging easier.
		panic("descriptor.File should not contain nil SourceCodeInfo")
	}

	outerPaths := make([]int32, len(outers))
	for i := range outers {
		location := ""
		if file.Package != nil {
			location = file.GetPackage()
		}

		msg, err := reg.LookupMsg(location, strings.Join(outers[:i+1], "."))
		if err != nil {
			panic(err)
		}
		outerPaths[i] = int32(msg.Index)
	}

	for _, loc := range file.SourceCodeInfo.Location {
		if !isProtoPathMatches(loc.Path, outerPaths, typeName, typeIndex, fieldPaths) {
			continue
		}
		comments := ""
		if loc.LeadingComments != nil {
			comments = strings.TrimRight(*loc.LeadingComments, "\n")
			comments = strings.TrimSpace(comments)
			// TODO(ivucica): this is a hack to fix "// " being interpreted as "//".
			// perhaps we should:
			// - split by \n
			// - determine if every (but first and last) line begins with " "
			// - trim every line only if that is the case
			// - join by \n
			comments = strings.Replace(comments, "\n ", "\n", -1)
		}
		return comments
	}
	return ""
}

var messageProtoPath = protoPathIndex(reflect.TypeOf((*pbdescriptor.FileDescriptorProto)(nil)), "MessageType")
var nestedProtoPath = protoPathIndex(reflect.TypeOf((*pbdescriptor.DescriptorProto)(nil)), "NestedType")
var packageProtoPath = protoPathIndex(reflect.TypeOf((*pbdescriptor.FileDescriptorProto)(nil)), "Package")

func isProtoPathMatches(paths []int32, outerPaths []int32, typeName string, typeIndex int32, fieldPaths []int32) bool {
	if typeName == "Package" && typeIndex == packageProtoPath {
		// path for package comments is just [2], and all the other processing
		// is too complex for it.
		if len(paths) == 0 || typeIndex != paths[0] {
			return false
		}
		return true
	}

	if len(paths) != len(outerPaths)*2+2+len(fieldPaths) {
		return false
	}

	typeNameDescriptor := reflect.TypeOf((*pbdescriptor.FileDescriptorProto)(nil))
	if len(outerPaths) > 0 {
		if paths[0] != messageProtoPath || paths[1] != outerPaths[0] {
			return false
		}
		paths = paths[2:]
		outerPaths = outerPaths[1:]

		for i, v := range outerPaths {
			if paths[i*2] != nestedProtoPath || paths[i*2+1] != v {
				return false
			}
		}
		paths = paths[len(outerPaths)*2:]

		if typeName == "MessageType" {
			typeName = "NestedType"
		}
		typeNameDescriptor = reflect.TypeOf((*pbdescriptor.DescriptorProto)(nil))
	}

	if paths[0] != protoPathIndex(typeNameDescriptor, typeName) || paths[1] != typeIndex {
		return false
	}
	paths = paths[2:]

	for i, v := range fieldPaths {
		if paths[i] != v {
			return false
		}
	}
	return true
}

// protoPathIndex returns a path component for google.protobuf.descriptor.SourceCode_Location.
//
// Specifically, it returns an id as generated from descriptor proto which
// can be used to determine what type the id following it in the path is.
// For example, if we are trying to locate comments related to a field named
// `Address` in a message named `Person`, the path will be:
//
//     [4, a, 2, b]
//
// While `a` gets determined by the order in which the messages appear in
// the proto file, and `b` is the field index specified in the proto
// file itself, the path actually needs to specify that `a` refers to a
// message and not, say, a service; and  that `b` refers to a field and not
// an option.
//
// protoPathIndex figures out the values 4 and 2 in the above example. Because
// messages are top level objects, the value of 4 comes from field id for
// `MessageType` inside `google.protobuf.descriptor.FileDescriptor` message.
// This field has a message type `google.protobuf.descriptor.DescriptorProto`.
// And inside message `DescriptorProto`, there is a field named `Field` with id
// 2.
//
// Some code generators seem to be hardcoding these values; this method instead
// interprets them from `descriptor.proto`-derived Go source as necessary.
func protoPathIndex(descriptorType reflect.Type, what string) int32 {
	field, ok := descriptorType.Elem().FieldByName(what)
	if !ok {
		panic(fmt.Errorf("could not find protobuf descriptor type id for %s", what))
	}
	pbtag := field.Tag.Get("protobuf")
	if pbtag == "" {
		panic(fmt.Errorf("no Go tag 'protobuf' on protobuf descriptor for %s", what))
	}
	path, err := strconv.Atoi(strings.Split(pbtag, ",")[1])
	if err != nil {
		panic(fmt.Errorf("protobuf descriptor id for %s cannot be converted to a number: %s", what, err.Error()))
	}

	return int32(path)
}
