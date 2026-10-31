package analysis

import (
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/validator"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

type analyzer struct {
	schema *ast.Schema
	doc    *ast.QueryDocument

	op    *ast.OperationDefinition
	frags map[string]*ast.FragmentDefinition

	clone *ast.QueryDocument
}

// responseName returns the response key used for a field selection.
func responseName(f *ast.Field) string {
	if f.Alias != "" {
		return f.Alias
	}
	return f.Name
}

// measure counts distinct response paths and maximum field depth on the raw
// operation structure, expanding named fragments the same way response
// collection does. Missing fragments contribute nothing (validation reports
// them afterwards) and a fragment already on the expansion stack is not
// re-entered, so a cycle cannot make this pass diverge.
func measure(op *ast.OperationDefinition, frags map[string]*ast.FragmentDefinition) (int, int) {
	paths := make(map[string]struct{})
	maxDepth := 0
	active := make(map[string]struct{})

	var walk func(ast.SelectionSet, string, int)
	walk = func(sels ast.SelectionSet, parent string, depth int) {
		for _, sel := range sels {
			switch s := sel.(type) {
			case *ast.Field:
				full := joinPath(parent, responseName(s))
				paths[full] = struct{}{}
				if depth+1 > maxDepth {
					maxDepth = depth + 1
				}
				walk(s.SelectionSet, full, depth+1)
			case *ast.InlineFragment:
				walk(s.SelectionSet, parent, depth)
			case *ast.FragmentSpread:
				f := frags[s.Name]
				if f == nil {
					continue
				}
				if _, onStack := active[f.Name]; onStack {
					continue
				}
				active[f.Name] = struct{}{}
				walk(f.SelectionSet, parent, depth)
				delete(active, f.Name)
			}
		}
	}

	walk(op.SelectionSet, "", 0)
	return len(paths), maxDepth
}

// validate clones the document, runs gqlparser's full default validation on
// the clone and keeps the type-resolved clone for snapshot collection. Only
// errors attributable to the selected operation (its own ranges or fragments
// it transitively reaches) fail the analysis; problems in sibling operations
// and unused fragments do not affect this operation's snapshot.
func (a *analyzer) validate() ErrorList {
	a.clone = cloneDocument(a.doc)
	// Full default rule set, the same validation gqlparser runs for LoadQuery.
	gqlErrs := validator.ValidateWithRules(a.schema, a.clone, rules.NewDefaultRules())
	if len(gqlErrs) == 0 {
		return nil
	}

	idx := a.buildIndex()

	var out ErrorList
	seen := make(map[string]struct{})
	for _, ge := range gqlErrs {
		scoped, path, locs := idx.resolve(ge)
		if !scoped {
			continue
		}
		e := &Error{
			Kind:      kindFor(ge.Rule, ge.Message),
			Message:   ge.Message,
			Operation: a.op.Name,
			Path:      path,
			Locations: locs,
		}
		key := strings.Join([]string{
			string(e.Kind),
			e.Message,
			strings.Join(e.Path, "."),
			locationKey(e.Locations),
		}, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	sortErrors(out)
	return out
}

func locationKey(locs []Location) string {
	parts := make([]string, 0, len(locs))
	for _, l := range locs {
		parts = append(parts, l.Source+":"+strconv.Itoa(l.Line)+":"+strconv.Itoa(l.Column))
	}
	return strings.Join(parts, ",")
}

func kindFor(rule, message string) ErrorKind {
	switch rule {
	case "OverlappingFieldsCanBeMerged":
		switch {
		case strings.Contains(message, "are different fields"):
			return ErrAliasConflict
		case strings.Contains(message, "differing arguments"):
			return ErrArgumentConflict
		default:
			return ErrFieldConflict
		}
	case "NoUndefinedVariables":
		return ErrUndefinedVariable
	case "KnownFragmentNames":
		return ErrUnknownFragment
	case "NoFragmentCycles":
		return ErrFragmentCycle
	case "FieldsOnCorrectType":
		return ErrFieldNotFound
	case "ScalarLeafs", "FragmentsOnCompositeTypes", "PossibleFragmentSpreads":
		return ErrSelectionTypeMismatch
	case "KnownRootType":
		return ErrRootOperationMissing
	case "LoneAnonymousOperation":
		return ErrAnonymousNotUnique
	default:
		return ErrValidation
	}
}

// ---------------------------------------------------------------------------
// Structural index used to scope and enrich validator errors.
// ---------------------------------------------------------------------------

type runeSpan struct {
	src        *ast.Source
	start, end int // rune offsets, inclusive at end
}

type fieldSpan struct {
	runeSpan
	path []string
}

type scopeIndex struct {
	a       *analyzer
	sources []*ast.Source
	spans   []runeSpan // operation, same-named operations, reachable fragments
	fields  []fieldSpan
	starts  map[*ast.Source][]int
}

func (a *analyzer) buildIndex() *scopeIndex {
	idx := &scopeIndex{
		a:      a,
		starts: make(map[*ast.Source][]int),
	}

	sourceSeen := make(map[*ast.Source]struct{})
	addSource := func(pos *ast.Position) {
		if pos == nil || pos.Src == nil {
			return
		}
		if _, ok := sourceSeen[pos.Src]; ok {
			return
		}
		sourceSeen[pos.Src] = struct{}{}
		idx.sources = append(idx.sources, pos.Src)
	}
	walkPositions(a.doc, addSource)

	addSpan := func(span runeSpan, ok bool) {
		if ok {
			idx.spans = append(idx.spans, span)
		}
	}

	// Parser positions are token-sized, so a span is computed from every
	// descendant position of its owner rather than the owner token alone.
	addSpan(operationSpan(a.op))
	// Duplicate operation names are document-level ambiguities: attribute an
	// error on a same-named sibling to this analysis even though only one of
	// them is selected.
	for _, other := range a.doc.Operations {
		if other != a.op && other.Name != "" && other.Name == a.op.Name {
			addSpan(operationSpan(other))
		}
	}

	// Reachable fragment closure, by name, cycle-safe.
	reachable := make(map[string]struct{})
	visited := make(map[string]struct{})
	var reach func(ast.SelectionSet)
	reach = func(sels ast.SelectionSet) {
		for _, sel := range sels {
			switch s := sel.(type) {
			case *ast.Field:
				reach(s.SelectionSet)
			case *ast.InlineFragment:
				reach(s.SelectionSet)
			case *ast.FragmentSpread:
				f := a.frags[s.Name]
				if f == nil {
					continue
				}
				if _, ok := visited[f.Name]; ok {
					continue
				}
				visited[f.Name] = struct{}{}
				reachable[f.Name] = struct{}{}
				reach(f.SelectionSet)
			}
		}
	}
	reach(a.op.SelectionSet)

	// Every definition carrying a reachable name is in scope (covers duplicate
	// fragment definitions).
	for _, f := range a.doc.Fragments {
		if _, ok := reachable[f.Name]; ok {
			addSpan(fragmentSpan(f))
		}
	}

	// Field intervals with their response paths, following the same fragment
	// expansion rules as measure. Each span covers the field's arguments,
	// directives and selection set, not only its name token.
	active := make(map[string]struct{})
	var indexFields func(ast.SelectionSet, string)
	indexFields = func(sels ast.SelectionSet, parent string) {
		for _, sel := range sels {
			switch s := sel.(type) {
			case *ast.Field:
				full := joinPath(parent, responseName(s))
				if span, ok := fieldSpanOf(s); ok {
					idx.fields = append(idx.fields, fieldSpan{
						runeSpan: span,
						path:     strings.Split(full, "."),
					})
				}
				indexFields(s.SelectionSet, full)
			case *ast.InlineFragment:
				indexFields(s.SelectionSet, parent)
			case *ast.FragmentSpread:
				f := a.frags[s.Name]
				if f == nil {
					continue
				}
				if _, onStack := active[f.Name]; onStack {
					continue
				}
				active[f.Name] = struct{}{}
				indexFields(f.SelectionSet, parent)
				delete(active, f.Name)
			}
		}
	}
	indexFields(a.op.SelectionSet, "")

	return idx
}

// resolve attributes one validator error to the analyzed operation. The
// second result is the innermost enclosing field response path. Locationless
// errors are kept (fail closed).
func (idx *scopeIndex) resolve(ge *gqlerror.Error) (bool, []string, []Location) {
	if len(ge.Locations) == 0 {
		return true, nil, nil
	}

	file, _ := ge.Extensions["file"].(string)
	src := idx.sourceFor(file)

	locs := make([]Location, 0, len(ge.Locations))
	for _, l := range ge.Locations {
		locs = append(locs, Location{Line: l.Line, Column: l.Column, Source: file})
	}

	primary := ge.Locations[0]
	if src == nil {
		// Source cannot be located; keep the error rather than silently
		// dropping it, but no path can be derived.
		return true, nil, locs
	}
	off, ok := idx.offset(src, primary.Line, primary.Column)
	if !ok {
		return true, nil, locs
	}

	scoped := false
	for _, sp := range idx.spans {
		if sp.src == src && off >= sp.start && off <= sp.end {
			scoped = true
			break
		}
	}

	// Among containing field spans, the innermost one starts latest.
	var best *fieldSpan
	for i := range idx.fields {
		fs := &idx.fields[i]
		if fs.src != src || off < fs.start || off > fs.end {
			continue
		}
		if best == nil || fs.start > best.start ||
			(fs.start == best.start && len(fs.path) > len(best.path)) {
			best = fs
		}
	}
	var path []string
	if best != nil {
		path = copyStrings(best.path)
	}

	return scoped, path, locs
}

func (idx *scopeIndex) sourceFor(file string) *ast.Source {
	var unnamed, first *ast.Source
	for _, src := range idx.sources {
		if first == nil {
			first = src
		}
		if src.Name == file {
			return src
		}
		if src.Name == "" && unnamed == nil {
			unnamed = src
		}
	}
	if file == "" && unnamed != nil {
		return unnamed
	}
	return first
}

// offset converts a 1-based line/column pair into a rune offset in src.
func (idx *scopeIndex) offset(src *ast.Source, line, column int) (int, bool) {
	if line <= 0 || column <= 0 {
		return 0, false
	}
	starts, ok := idx.starts[src]
	if !ok {
		starts = lineStarts(src)
		idx.starts[src] = starts
	}
	if line > len(starts) {
		return 0, false
	}
	off := starts[line-1] + column - 1
	runeCount := 0
	for range src.Input {
		runeCount++
	}
	if off < 0 || off > runeCount {
		return 0, false
	}
	return off, true
}

func lineStarts(src *ast.Source) []int {
	starts := []int{0}
	runes := 0
	for _, r := range src.Input {
		if r == '\n' {
			starts = append(starts, runes+1)
		}
		runes++
	}
	return starts
}

// visitValuePositions visits every position in a value tree.
func visitValuePositions(v *ast.Value, f func(*ast.Position)) {
	if v == nil {
		return
	}
	f(v.Position)
	for _, c := range v.Children {
		f(c.Position)
		visitValuePositions(c.Value, f)
	}
}

func visitArgumentPositions(args ast.ArgumentList, f func(*ast.Position)) {
	for _, a := range args {
		f(a.Position)
		visitValuePositions(a.Value, f)
	}
}

func visitDirectivePositions(dirs ast.DirectiveList, f func(*ast.Position)) {
	for _, d := range dirs {
		f(d.Position)
		visitArgumentPositions(d.Arguments, f)
	}
}

func visitSelectionPositions(sels ast.SelectionSet, f func(*ast.Position)) {
	for _, sel := range sels {
		switch s := sel.(type) {
		case *ast.Field:
			f(s.Position)
			visitArgumentPositions(s.Arguments, f)
			visitDirectivePositions(s.Directives, f)
			visitSelectionPositions(s.SelectionSet, f)
		case *ast.FragmentSpread:
			f(s.Position)
			visitDirectivePositions(s.Directives, f)
		case *ast.InlineFragment:
			f(s.Position)
			visitDirectivePositions(s.Directives, f)
			visitSelectionPositions(s.SelectionSet, f)
		}
	}
}

func visitTypePositions(t *ast.Type, f func(*ast.Position)) {
	if t == nil {
		return
	}
	f(t.Position)
	visitTypePositions(t.Elem, f)
}

// walkPositions visits every source position in a query document.
func walkPositions(doc *ast.QueryDocument, f func(*ast.Position)) {
	for _, op := range doc.Operations {
		f(op.Position)
		for _, v := range op.VariableDefinitions {
			f(v.Position)
			visitTypePositions(v.Type, f)
			visitValuePositions(v.DefaultValue, f)
			visitDirectivePositions(v.Directives, f)
		}
		visitDirectivePositions(op.Directives, f)
		visitSelectionPositions(op.SelectionSet, f)
	}
	for _, frag := range doc.Fragments {
		f(frag.Position)
		visitDirectivePositions(frag.Directives, f)
		visitSelectionPositions(frag.SelectionSet, f)
	}
}

// extentOf computes the rune range, within the first source seen, covered by
// the emitted positions.
func extentOf(emit func(func(*ast.Position))) (runeSpan, bool) {
	span := runeSpan{}
	started := false
	add := func(pos *ast.Position) {
		if pos == nil || pos.Src == nil {
			return
		}
		if !started {
			span.src = pos.Src
			span.start, span.end = pos.Start, pos.End
			started = true
			return
		}
		if pos.Src != span.src {
			return
		}
		if pos.Start < span.start {
			span.start = pos.Start
		}
		if pos.End > span.end {
			span.end = pos.End
		}
	}
	emit(add)
	return span, started
}

// operationSpan covers the operation keyword, its variables, directives and
// selections.
func operationSpan(op *ast.OperationDefinition) (runeSpan, bool) {
	return extentOf(func(add func(*ast.Position)) {
		add(op.Position)
		for _, v := range op.VariableDefinitions {
			add(v.Position)
			visitTypePositions(v.Type, add)
			visitValuePositions(v.DefaultValue, add)
			visitDirectivePositions(v.Directives, add)
		}
		visitDirectivePositions(op.Directives, add)
		visitSelectionPositions(op.SelectionSet, add)
	})
}

func fragmentSpan(frag *ast.FragmentDefinition) (runeSpan, bool) {
	return extentOf(func(add func(*ast.Position)) {
		add(frag.Position)
		visitDirectivePositions(frag.Directives, add)
		visitSelectionPositions(frag.SelectionSet, add)
	})
}

func fieldSpanOf(f *ast.Field) (runeSpan, bool) {
	return extentOf(func(add func(*ast.Position)) {
		add(f.Position)
		visitArgumentPositions(f.Arguments, add)
		visitDirectivePositions(f.Directives, add)
		visitSelectionPositions(f.SelectionSet, add)
	})
}
