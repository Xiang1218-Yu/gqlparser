// Package analysis provides an opt-in, immutable snapshot of a single GraphQL
// operation.
//
// The analyzer is disabled by default: nothing in the parser, ast or
// validator packages changes behaviour when this package is not used.
// [Analyze] never executes resolvers and never mutates the schema, query
// document, AST values, fragments or the [Options] value handed in by the
// caller. Analysis reuses gqlparser's own parser, validator and error
// semantics: a document that fails full query validation yields structured
// [Error] values and no partial snapshot.
package analysis

import (
	"sort"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// Options configures a single [Analyze] call. The zero value is valid and
// applies no limits. Analyze treats Options as read-only; callers may reuse
// the same value concurrently and mutate it once the call has returned.
type Options struct {
	// MaxFields limits the number of distinct field response paths in the
	// analyzed operation, counted after fragment expansion and merging of
	// compatible selections. Zero disables the limit.
	MaxFields int
	// MaxDepth limits the maximum field nesting depth, measured in response
	// paths. Root fields have depth 1; fragments and inline fragments do not
	// add levels themselves. Zero disables the limit.
	MaxDepth int
}

// Location is an immutable copy of an ast source position.
type Location struct {
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Source string `json:"source,omitempty"`
}

// Argument is a canonical, immutable argument representation. Value uses
// GraphQL literal syntax with input object fields rendered in name order, so
// the same logical value always renders identically.
type Argument struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// DirectiveUse is one occurrence of a directive in the analyzed operation.
type DirectiveUse struct {
	Name      string     `json:"name"`
	Arguments []Argument `json:"arguments,omitempty"`
	Locations []Location `json:"locations,omitempty"`
}

// Variable describes one operation variable definition.
type Variable struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Required   bool   `json:"required"`
	HasDefault bool   `json:"hasDefault"`
	// Default is the canonical rendering of the default literal. It is empty
	// when HasDefault is false.
	Default    string         `json:"default,omitempty"`
	Directives []DirectiveUse `json:"directives,omitempty"`
	Location   Location       `json:"location"`
}

// FragmentSpread records one place where a named fragment is referenced.
type FragmentSpread struct {
	Path       []string       `json:"path"`
	Directives []DirectiveUse `json:"directives,omitempty"`
	Location   Location       `json:"location"`
}

// Fragment describes a named fragment reachable from the analyzed operation,
// in the order the operation first reaches it.
type Fragment struct {
	Name          string           `json:"name"`
	TypeCondition string           `json:"typeCondition"`
	Directives    []DirectiveUse   `json:"directives,omitempty"`
	Location      Location         `json:"location"`
	Spreads       []FragmentSpread `json:"spreads,omitempty"`
}

// Field is one merged field in the operation response shape. Fields reached
// through compatible selections, inline fragments or named fragments are
// merged by response path.
type Field struct {
	// NodeID is a stable, content-derived identifier for the field response
	// path. Repeating analysis over the same schema, document and operation
	// name always yields the same NodeID for the same logical field,
	// regardless of formatting or fragment definition order.
	NodeID string `json:"nodeId"`
	// ResponsePath holds the response keys (aliases where present) from the
	// operation root to the field.
	ResponsePath []string `json:"responsePath"`
	// Name is the schema field name.
	Name string `json:"name"`
	// Alias is the response alias when one was requested, otherwise empty.
	Alias string `json:"alias"`
	// ResponseName is the key the field occupies in the response.
	ResponseName string `json:"responseName"`
	// Type is the canonical GraphQL return type, for example "[Character!]!".
	Type string `json:"type"`
	// ParentTypes are the object or interface types on which the field was
	// selected, sorted and deduplicated.
	ParentTypes []string `json:"parentTypes,omitempty"`
	// TypeConditions are the inline and named fragment type conditions under
	// which the field was selected, sorted and deduplicated. It is empty for
	// fields selected unconditionally.
	TypeConditions []string       `json:"typeConditions,omitempty"`
	Arguments      []Argument     `json:"arguments,omitempty"`
	Directives     []DirectiveUse `json:"directives,omitempty"`
	Locations      []Location     `json:"locations,omitempty"`
	Fields         []*Field       `json:"fields,omitempty"`
}

// Snapshot is the immutable result of analyzing one operation. Snapshot
// values and everything reachable from them are copies: retaining or sharing
// a Snapshot never affects later analysis calls or the input AST.
type Snapshot struct {
	// Operation is the operation name; empty for an anonymous operation.
	Operation string `json:"operation,omitempty"`
	// OperationType is "query", "mutation" or "subscription". Anonymous
	// shorthand operations report "query".
	OperationType string `json:"operationType"`
	// RootType is the name of the schema root object type.
	RootType string `json:"rootType"`

	// Variables lists variable definitions in document order.
	Variables []Variable `json:"variables,omitempty"`
	// Directives are the directives applied to the operation itself.
	Directives []DirectiveUse `json:"directives,omitempty"`
	// Fragments lists every named fragment transitively referenced by the
	// operation, in first-reference order.
	Fragments []Fragment `json:"fragments,omitempty"`
	// Fields is the merged root selection, sorted by response name.
	Fields []*Field `json:"fields"`

	// FieldCount is the number of distinct field response paths.
	FieldCount int `json:"fieldCount"`
	// MaxDepth is the greatest field nesting depth; a root-only operation
	// has depth 1.
	MaxDepth int `json:"maxDepth"`

	// Summary is a deterministic textual digest of the snapshot. The same
	// logical operation always produces the same summary regardless of
	// whitespace, comments, fragment definition order or input object field
	// order.
	Summary string `json:"summary"`
}

// ErrorKind identifies why analysis failed. Kinds stay stable across releases
// unless a breaking change is announced.
type ErrorKind string

const (
	// ErrInvalidInput: nil schema/document or malformed options.
	ErrInvalidInput ErrorKind = "INVALID_INPUT"
	// ErrParseError: the query source given to the parsing convenience entry
	// point could not be parsed.
	ErrParseError ErrorKind = "PARSE_ERROR"
	// ErrOperationNotFound: no operation matched the requested name.
	ErrOperationNotFound ErrorKind = "OPERATION_NOT_FOUND"
	// ErrAnonymousNotUnique: an anonymous operation shares the document with
	// other operations.
	ErrAnonymousNotUnique ErrorKind = "ANONYMOUS_OPERATION_NOT_UNIQUE"
	// ErrMaxFieldsExceeded: the operation has more fields than Options.MaxFields.
	ErrMaxFieldsExceeded ErrorKind = "MAX_FIELDS_EXCEEDED"
	// ErrMaxDepthExceeded: the operation nests deeper than Options.MaxDepth.
	ErrMaxDepthExceeded ErrorKind = "MAX_DEPTH_EXCEEDED"
	// ErrAliasConflict: the same response name resolves to different fields.
	ErrAliasConflict ErrorKind = "ALIAS_CONFLICT"
	// ErrArgumentConflict: the same response name is requested with
	// incompatible arguments.
	ErrArgumentConflict ErrorKind = "ARGUMENT_CONFLICT"
	// ErrFieldConflict: merged selections disagree in a way other than alias
	// or arguments, for example return type.
	ErrFieldConflict ErrorKind = "FIELD_CONFLICT"
	// ErrUndefinedVariable: a variable reference has no definition.
	ErrUndefinedVariable ErrorKind = "UNDEFINED_VARIABLE"
	// ErrUnknownFragment: a fragment spread names no defined fragment.
	ErrUnknownFragment ErrorKind = "UNKNOWN_FRAGMENT"
	// ErrFragmentCycle: fragments spread themselves transitively.
	ErrFragmentCycle ErrorKind = "FRAGMENT_CYCLE"
	// ErrFieldNotFound: a field does not exist on its selection type.
	ErrFieldNotFound ErrorKind = "FIELD_NOT_FOUND"
	// ErrSelectionTypeMismatch: a leaf/composite selection set does not match
	// the field type, or a fragment conditions on or spreads over an
	// incompatible type.
	ErrSelectionTypeMismatch ErrorKind = "SELECTION_SET_TYPE_MISMATCH"
	// ErrRootOperationMissing: the schema does not define the operation's root
	// type.
	ErrRootOperationMissing ErrorKind = "ROOT_OPERATION_TYPE_MISSING"
	// ErrValidation: a full validation failure without a more specific kind.
	ErrValidation ErrorKind = "VALIDATION_ERROR"
)

// Error is one structured analysis failure. Analysis never returns partial
// snapshots: when ErrorList is non-empty the snapshot is nil.
type Error struct {
	// Kind is the machine-readable failure category.
	Kind ErrorKind `json:"kind"`
	// Message is the concrete reason, reusing gqlparser's validator messages
	// when the failure comes from validation.
	Message string `json:"message"`
	// Operation is the requested operation name, empty for anonymous
	// operations.
	Operation string `json:"operation,omitempty"`
	// Path is the field response path at which the error applies, when one
	// exists.
	Path []string `json:"path,omitempty"`
	// Locations lists the relevant source positions, with the primary
	// position first.
	Locations []Location `json:"locations,omitempty"`
}

func (e *Error) Error() string {
	var b strings.Builder
	if len(e.Locations) > 0 {
		loc := e.Locations[0]
		if loc.Source != "" {
			b.WriteString(loc.Source)
		} else {
			b.WriteString("input")
		}
		if loc.Line > 0 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(loc.Line))
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(loc.Column))
		}
		b.WriteString(": ")
	}
	if len(e.Path) > 0 {
		b.WriteString(strings.Join(e.Path, "."))
		b.WriteByte(' ')
	}
	b.WriteString(e.Message)
	return b.String()
}

// ErrorList is the ordered set of failures from [Analyze].
type ErrorList []*Error

func (el ErrorList) Error() string {
	var b strings.Builder
	for i, e := range el {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Error())
	}
	return b.String()
}

// Analyze produces an immutable [Snapshot] for the named operation in an
// already parsed document, using schema for type information. An empty
// operationName selects the document's single operation, including an
// anonymous one. opts may be nil for unlimited analysis.
//
// Analysis first enforces any field and depth limits, then runs gqlparser's
// full query validation on an isolated copy of the document. Validation or
// limit failures return a non-empty [ErrorList] and a nil snapshot; no
// partial results are retained.
func Analyze(
	schema *ast.Schema,
	doc *ast.QueryDocument,
	operationName string,
	opts *Options,
) (*Snapshot, ErrorList) {
	var errs ErrorList
	if schema == nil {
		errs = append(errs, &Error{Kind: ErrInvalidInput, Message: "cannot analyze: schema is nil"})
	}
	if doc == nil {
		errs = append(
			errs,
			&Error{Kind: ErrInvalidInput, Message: "cannot analyze: query document is nil"},
		)
	}
	if opts != nil && (opts.MaxFields < 0 || opts.MaxDepth < 0) {
		errs = append(errs, &Error{
			Kind:    ErrInvalidInput,
			Message: "cannot analyze: max fields and max depth must be >= 0",
		})
	}
	if len(errs) > 0 {
		return nil, errs
	}

	limits := Options{}
	if opts != nil {
		limits = *opts // copy: the caller's Options is never read again or mutated
	}

	a := &analyzer{
		schema: schema,
		doc:    doc,
	}

	op, selErrs := a.selectOperation(operationName)
	if len(selErrs) > 0 {
		return nil, selErrs
	}
	a.op = op

	// Limits are enforced on the raw structure before any validation or
	// snapshot work, so an over-limit operation fails fast regardless of any
	// other problems in the document.
	count, depth := measure(op, a.frags)
	if limits.MaxFields > 0 && count > limits.MaxFields {
		errs = append(errs, &Error{
			Kind: ErrMaxFieldsExceeded,
			Message: "operation has " + strconv.Itoa(count) +
				" fields, maximum is " + strconv.Itoa(limits.MaxFields),
			Operation: op.Name,
			Locations: locsOf(op.Position),
		})
	}
	if limits.MaxDepth > 0 && depth > limits.MaxDepth {
		errs = append(errs, &Error{
			Kind: ErrMaxDepthExceeded,
			Message: "operation has depth " + strconv.Itoa(depth) +
				", maximum is " + strconv.Itoa(limits.MaxDepth),
			Operation: op.Name,
			Locations: locsOf(op.Position),
		})
	}
	if len(errs) > 0 {
		return nil, errs
	}

	if verrs := a.validate(); len(verrs) > 0 {
		return nil, verrs
	}

	return a.buildSnapshot()
}

// selectOperation resolves the requested operation using the same lookup
// semantics as ast.OperationList.ForName, while turning ambiguous anonymous
// documents into structured errors.
func (a *analyzer) selectOperation(name string) (*ast.OperationDefinition, ErrorList) {
	a.frags = make(map[string]*ast.FragmentDefinition, len(a.doc.Fragments))
	for _, f := range a.doc.Fragments {
		if _, ok := a.frags[f.Name]; !ok {
			a.frags[f.Name] = f
		}
	}

	if name == "" && len(a.doc.Operations) == 1 {
		return a.doc.Operations[0], nil
	}
	if name != "" {
		if op := a.doc.Operations.ForName(name); op != nil {
			return op, nil
		}
		return nil, ErrorList{&Error{
			Kind:      ErrOperationNotFound,
			Message:   `operation "` + name + `" not found in query document`,
			Operation: name,
		}}
	}

	switch len(a.doc.Operations) {
	case 0:
		return nil, ErrorList{&Error{
			Kind:    ErrOperationNotFound,
			Message: "query document contains no operations",
		}}
	default:
		var anonymous []*ast.OperationDefinition
		for _, op := range a.doc.Operations {
			if op.Name == "" {
				anonymous = append(anonymous, op)
			}
		}
		if len(anonymous) > 0 {
			errs := make(ErrorList, 0, len(anonymous))
			for _, op := range anonymous {
				errs = append(errs, &Error{
					Kind:      ErrAnonymousNotUnique,
					Message:   "this anonymous operation must be the only defined operation",
					Operation: name,
					Locations: locsOf(op.Position),
				})
			}
			sortErrors(errs)
			return nil, errs
		}
		return nil, ErrorList{&Error{
			Kind: ErrOperationNotFound,
			Message: "an operation name is required when the document contains " +
				"more than one operation",
		}}
	}
}

func sortErrors(errs ErrorList) {
	sort.SliceStable(errs, func(i, j int) bool {
		li, lj := errs[i].Locations, errs[j].Locations
		if len(li) == 0 || len(lj) == 0 {
			return errs[i].Message < errs[j].Message
		}
		if li[0].Source != lj[0].Source {
			return li[0].Source < lj[0].Source
		}
		if li[0].Line != lj[0].Line {
			return li[0].Line < lj[0].Line
		}
		if li[0].Column != lj[0].Column {
			return li[0].Column < lj[0].Column
		}
		if errs[i].Kind != errs[j].Kind {
			return errs[i].Kind < errs[j].Kind
		}
		return errs[i].Message < errs[j].Message
	})
}
