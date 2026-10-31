package analysis

import (
	"hash/fnv"
	"slices"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

type fieldOccurrence struct {
	loc     Location
	parent  string
	conds   []string
	args    []Argument
	dirUses []DirectiveUse
}

type fieldBuilder struct {
	key  string
	path []string
	name string
	rn   string
	typ  string

	occ      []fieldOccurrence
	children map[string]*fieldBuilder
}

type fragmentRecord struct {
	def     *ast.FragmentDefinition
	spreads []FragmentSpread
}

type collector struct {
	a     *analyzer
	frags map[string]*ast.FragmentDefinition
	nodes map[string]*fieldBuilder
	roots map[string]*fieldBuilder
	seen  map[string]*fragmentRecord
	order []string
}

func (a *analyzer) buildSnapshot() (*Snapshot, ErrorList) {
	c := &collector{
		a:     a,
		frags: make(map[string]*ast.FragmentDefinition, len(a.clone.Fragments)),
		nodes: make(map[string]*fieldBuilder),
		roots: make(map[string]*fieldBuilder),
		seen:  make(map[string]*fragmentRecord),
	}
	for _, f := range a.clone.Fragments {
		if _, ok := c.frags[f.Name]; !ok {
			c.frags[f.Name] = f
		}
	}

	opType := string(a.op.Operation)
	var root *ast.Definition
	switch a.op.Operation {
	case ast.Query, "":
		opType = string(ast.Query)
		root = a.schema.Query
	case ast.Mutation:
		root = a.schema.Mutation
	case ast.Subscription:
		root = a.schema.Subscription
	}
	if root == nil {
		return nil, ErrorList{&Error{
			Kind:      ErrRootOperationMissing,
			Message:   `schema does not support operation type "` + opType + `"`,
			Operation: a.op.Name,
			Locations: locsOf(a.op.Position),
		}}
	}

	if errs := c.walk(a.op.SelectionSet, root, "", nil, nil); len(errs) > 0 {
		return nil, errs
	}

	snap := &Snapshot{
		Operation:     a.op.Name,
		OperationType: opType,
		RootType:      root.Name,
		Variables:     c.collectVariables(),
		Directives:    directiveUsesOf(a.op.Directives),
		Fragments:     c.collectFragments(),
		FieldCount:    len(c.nodes),
	}
	snap.Fields, snap.MaxDepth = c.buildFields()
	snap.Summary = c.renderSummary(snap)
	return snap, nil
}

func (c *collector) walk(
	sels ast.SelectionSet,
	parent *ast.Definition,
	parentKey string,
	conds []string,
	active map[string]struct{},
) ErrorList {
	var errs ErrorList
	for _, sel := range sels {
		switch s := sel.(type) {
		case *ast.Field:
			rn := responseName(s)
			key := joinPath(parentKey, rn)
			fb := c.ensureField(key, rn)

			if fb.typ == "" {
				fb.name = s.Name
				fb.typ = fieldTypeName(c.a.schema, parent, s)
			}
			parentName := ""
			if parent != nil {
				parentName = parent.Name
			}
			fb.occ = append(fb.occ, fieldOccurrence{
				loc:     locationOf(s.Position),
				parent:  parentName,
				conds:   copyStrings(conds),
				args:    argumentsOf(s.Arguments),
				dirUses: directiveUsesOf(s.Directives),
			})

			next := nextParentType(c.a.schema, parent, s)
			if next == nil && s.Name != "__typename" && len(s.SelectionSet) > 0 {
				errs = append(errs, &Error{
					Kind:      ErrFieldNotFound,
					Message:   `cannot resolve field "` + s.Name + `" on type "` + parentName + `"`,
					Operation: c.a.op.Name,
					Path:      strings.Split(key, "."),
					Locations: locsOf(s.Position),
				})
				continue
			}
			errs = append(errs, c.walk(s.SelectionSet, next, key, conds, active)...)

		case *ast.InlineFragment:
			next := parent
			nextConds := conds
			if s.TypeCondition != "" {
				next = c.a.schema.Types[s.TypeCondition]
				nextConds = withCondition(conds, s.TypeCondition, parent)
			}
			errs = append(errs, c.walk(s.SelectionSet, next, parentKey, nextConds, active)...)

		case *ast.FragmentSpread:
			f := c.frags[s.Name]
			if f == nil {
				errs = append(errs, &Error{
					Kind:      ErrUnknownFragment,
					Message:   `unknown fragment "` + s.Name + `"`,
					Operation: c.a.op.Name,
					Path:      splitPath(parentKey),
					Locations: locsOf(s.Position),
				})
				continue
			}
			rec := c.seen[f.Name]
			if rec == nil {
				rec = &fragmentRecord{def: f}
				c.seen[f.Name] = rec
				c.order = append(c.order, f.Name)
			}
			rec.spreads = append(rec.spreads, FragmentSpread{
				Path:       splitPath(parentKey),
				Directives: directiveUsesOf(s.Directives),
				Location:   locationOf(s.Position),
			})

			if active == nil {
				active = make(map[string]struct{})
			}
			if _, onStack := active[f.Name]; onStack {
				// Validation rejects cycles; keep collection terminating.
				continue
			}
			active[f.Name] = struct{}{}
			next := c.a.schema.Types[f.TypeCondition]
			errs = append(errs, c.walk(
				f.SelectionSet, next, parentKey,
				withCondition(conds, f.TypeCondition, parent), active,
			)...)
			delete(active, f.Name)
		}
	}
	return errs
}

func (c *collector) ensureField(key, rn string) *fieldBuilder {
	if fb, ok := c.nodes[key]; ok {
		return fb
	}
	fb := &fieldBuilder{
		key:      key,
		path:     strings.Split(key, "."),
		rn:       rn,
		children: make(map[string]*fieldBuilder),
	}
	c.nodes[key] = fb
	if parentKey := parentKeyOf(key); parentKey == "" {
		c.roots[key] = fb
	} else {
		parent := c.nodes[parentKey]
		parent.children[rn] = fb
	}
	return fb
}

func parentKeyOf(key string) string {
	if i := strings.LastIndex(key, "."); i >= 0 {
		return key[:i]
	}
	return ""
}

func splitPath(key string) []string {
	if key == "" {
		return nil
	}
	return strings.Split(key, ".")
}

// withCondition appends condition when it actually narrows the current
// selection type: a fragment conditioned on the same type as its spread site
// adds no restriction and must not be recorded.
func withCondition(conds []string, condition string, parent *ast.Definition) []string {
	if condition == "" {
		return conds
	}
	if parent != nil && condition == parent.Name {
		return conds
	}
	if slices.Contains(conds, condition) {
		return conds
	}
	return append(append(make([]string, 0, len(conds)+1), conds...), condition)
}

// fieldTypeName reproduces the walker's field resolution for the type string.
func fieldTypeName(schema *ast.Schema, parent *ast.Definition, f *ast.Field) string {
	if f.Name == "__typename" {
		return "String"
	}
	if parent != nil {
		if def := parent.Fields.ForName(f.Name); def != nil {
			return def.Type.String()
		}
	}
	return ""
}

func nextParentType(schema *ast.Schema, parent *ast.Definition, f *ast.Field) *ast.Definition {
	if f.Name == "__typename" {
		return nil
	}
	if parent != nil {
		if def := parent.Fields.ForName(f.Name); def != nil {
			return schema.Types[def.Type.Name()]
		}
	}
	return nil
}

func (c *collector) collectVariables() []Variable {
	if len(c.a.op.VariableDefinitions) == 0 {
		return nil
	}
	out := make([]Variable, 0, len(c.a.op.VariableDefinitions))
	for _, v := range c.a.op.VariableDefinitions {
		info := Variable{
			Name:       v.Variable,
			Type:       v.Type.String(),
			Required:   v.Type.NonNull && v.DefaultValue == nil,
			Directives: directiveUsesOf(v.Directives),
			Location:   locationOf(v.Position),
		}
		if v.DefaultValue != nil {
			info.HasDefault = true
			info.Default = canonicalValue(v.DefaultValue)
		}
		out = append(out, info)
	}
	return out
}

func (c *collector) collectFragments() []Fragment {
	if len(c.order) == 0 {
		return nil
	}
	out := make([]Fragment, 0, len(c.order))
	for _, name := range c.order {
		rec := c.seen[name]
		spreads := make([]FragmentSpread, len(rec.spreads))
		for i, sp := range rec.spreads {
			spreads[i] = FragmentSpread{
				Path:       copyStrings(sp.Path),
				Directives: cloneDirectiveUses(sp.Directives),
				Location:   sp.Location,
			}
		}
		out = append(out, Fragment{
			Name:          rec.def.Name,
			TypeCondition: rec.def.TypeCondition,
			Directives:    directiveUsesOf(rec.def.Directives),
			Location:      locationOf(rec.def.Position),
			Spreads:       spreads,
		})
	}
	return out
}

func cloneDirectiveUses(in []DirectiveUse) []DirectiveUse {
	if len(in) == 0 {
		return nil
	}
	out := make([]DirectiveUse, len(in))
	for i, d := range in {
		out[i] = DirectiveUse{
			Name:      d.Name,
			Arguments: append([]Argument(nil), d.Arguments...),
			Locations: append([]Location(nil), d.Locations...),
		}
	}
	return out
}

// nodeIDs assigns a stable content hash id to every response path.
func (c *collector) nodeIDs() map[string]string {
	keys := make([]string, 0, len(c.nodes))
	for key := range c.nodes {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	ids := make(map[string]string, len(keys))
	byHash := make(map[string][]string)
	hashes := make(map[string]string, len(keys))
	for _, key := range keys {
		h := fnv.New64a()
		h.Write([]byte(key))
		hex := strconv.FormatUint(h.Sum64(), 16)
		hashes[key] = hex
		byHash[hex] = append(byHash[hex], key)
	}
	for hex, collided := range byHash {
		if len(collided) == 1 {
			ids[collided[0]] = hex
			continue
		}
		for i, key := range collided {
			if i == 0 {
				ids[key] = hex
				continue
			}
			ids[key] = hex + "-" + strconv.Itoa(i+1)
		}
	}
	return ids
}

func (c *collector) buildFields() ([]*Field, int) {
	ids := c.nodeIDs()
	maxDepth := 0
	for _, fb := range c.nodes {
		if len(fb.path) > maxDepth {
			maxDepth = len(fb.path)
		}
	}

	rootKeys := make([]string, 0, len(c.roots))
	for key := range c.roots {
		rootKeys = append(rootKeys, key)
	}
	slices.Sort(rootKeys)

	roots := make([]*Field, 0, len(rootKeys))
	for _, key := range rootKeys {
		roots = append(roots, c.buildField(c.roots[key], ids))
	}
	return roots, maxDepth
}

func (c *collector) buildField(fb *fieldBuilder, ids map[string]string) *Field {
	locs := make([]Location, 0, len(fb.occ))
	conds := make([]string, 0)
	parents := make([]string, 0)
	var dirs []DirectiveUse
	var args []Argument
	for i, occ := range fb.occ {
		locs = append(locs, occ.loc)
		conds = append(conds, occ.conds...)
		if occ.parent != "" {
			parents = append(parents, occ.parent)
		}
		dirs = append(dirs, occ.dirUses...)
		if i == 0 {
			args = append(args, occ.args...)
		}
	}

	childKeys := make([]string, 0, len(fb.children))
	for key := range fb.children {
		childKeys = append(childKeys, key)
	}
	slices.Sort(childKeys)
	children := make([]*Field, 0, len(childKeys))
	for _, key := range childKeys {
		children = append(children, c.buildField(fb.children[key], ids))
	}

	alias := ""
	if fb.rn != fb.name {
		alias = fb.rn
	}

	return &Field{
		NodeID:         ids[fb.key],
		ResponsePath:   copyStrings(fb.path),
		Name:           fb.name,
		Alias:          alias,
		ResponseName:   fb.rn,
		Type:           fb.typ,
		ParentTypes:    sortedUnique(parents),
		TypeConditions: sortedUnique(conds),
		Arguments:      args,
		Directives:     mergeDirectiveUses(dirs),
		Locations:      dedupLocations(locs),
		Fields:         children,
	}
}

func mergeDirectiveUses(in []DirectiveUse) []DirectiveUse {
	if len(in) == 0 {
		return nil
	}
	out := make([]DirectiveUse, 0, len(in))
	seen := make(map[string]struct{})
	for _, d := range in {
		key := directiveUseKey(d)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, d)
	}
	sortDirectiveUses(out)
	return out
}

func directiveUseKey(d DirectiveUse) string {
	parts := make([]string, 0, len(d.Arguments))
	for _, a := range d.Arguments {
		parts = append(parts, a.Name+":"+a.Value)
	}
	return d.Name + "(" + strings.Join(parts, ",") + ")" +
		"@" + locationKey(d.Locations)
}

func (c *collector) renderSummary(snap *Snapshot) string {
	var b strings.Builder

	name := snap.Operation
	if name == "" {
		name = "<anonymous>"
	}
	b.WriteString(snap.OperationType)
	b.WriteByte(' ')
	b.WriteString(name)
	b.WriteByte('\n')
	b.WriteString("root ")
	b.WriteString(snap.RootType)
	b.WriteByte('\n')

	for _, v := range snap.Variables {
		b.WriteString("var $")
		b.WriteString(v.Name)
		b.WriteString(": ")
		b.WriteString(v.Type)
		if v.HasDefault {
			b.WriteString(" = ")
			b.WriteString(v.Default)
		}
		b.WriteByte('\n')
	}
	for _, d := range snap.Directives {
		b.WriteString("directive ")
		b.WriteString(renderDirective(d))
		b.WriteByte('\n')
	}
	for _, f := range snap.Fragments {
		b.WriteString("fragment ")
		b.WriteString(f.Name)
		b.WriteString(" on ")
		b.WriteString(f.TypeCondition)
		b.WriteByte('\n')
	}
	b.WriteString("fields ")
	b.WriteString(strconv.Itoa(snap.FieldCount))
	b.WriteString(" depth ")
	b.WriteString(strconv.Itoa(snap.MaxDepth))
	b.WriteByte('\n')

	var render func(fields []*Field, indent string)
	render = func(fields []*Field, indent string) {
		for _, f := range fields {
			b.WriteString(indent)
			if f.Alias != "" {
				b.WriteString(f.Alias)
				b.WriteByte(':')
				b.WriteString(f.Name)
			} else {
				b.WriteString(f.Name)
			}
			if len(f.Arguments) > 0 {
				parts := make([]string, 0, len(f.Arguments))
				for _, a := range f.Arguments {
					parts = append(parts, a.Name+":"+a.Value)
				}
				b.WriteByte('(')
				b.WriteString(strings.Join(parts, ","))
				b.WriteByte(')')
			}
			if f.Type != "" {
				b.WriteString(": ")
				b.WriteString(f.Type)
			}
			if len(f.TypeConditions) > 0 {
				b.WriteString(" on ")
				b.WriteString(strings.Join(f.TypeConditions, ","))
			}
			for _, d := range f.Directives {
				b.WriteByte(' ')
				b.WriteString(renderDirective(d))
			}
			b.WriteByte('\n')
			render(f.Fields, indent+"  ")
		}
	}
	render(snap.Fields, "")

	return strings.TrimRight(b.String(), "\n")
}

func renderDirective(d DirectiveUse) string {
	var b strings.Builder
	b.WriteByte('@')
	b.WriteString(d.Name)
	if len(d.Arguments) > 0 {
		parts := make([]string, 0, len(d.Arguments))
		for _, a := range d.Arguments {
			parts = append(parts, a.Name+":"+a.Value)
		}
		b.WriteByte('(')
		b.WriteString(strings.Join(parts, ","))
		b.WriteByte(')')
	}
	return b.String()
}
