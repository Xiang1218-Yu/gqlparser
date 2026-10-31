package analysis

import "github.com/vektah/gqlparser/v2/ast"

// cloneDocument returns a structurally independent copy of doc. The
// validator's walker annotates AST nodes (Definition, ObjectDefinition,
// Used and friends), so analysis validates and resolves types against the
// clone. Position and CommentGroup pointers stay shared: they describe the
// immutable source and nothing in analysis writes them.
func cloneDocument(doc *ast.QueryDocument) *ast.QueryDocument {
	clone := &ast.QueryDocument{
		Operations: make(ast.OperationList, 0, len(doc.Operations)),
		Fragments:  make(ast.FragmentDefinitionList, 0, len(doc.Fragments)),
		Position:   doc.Position,
		Comment:    doc.Comment,
	}
	for _, op := range doc.Operations {
		clone.Operations = append(clone.Operations, cloneOperation(op))
	}
	for _, f := range doc.Fragments {
		clone.Fragments = append(clone.Fragments, cloneFragmentDefinition(f))
	}
	return clone
}

func cloneOperation(op *ast.OperationDefinition) *ast.OperationDefinition {
	return &ast.OperationDefinition{
		Description:         op.Description,
		Operation:           op.Operation,
		Name:                op.Name,
		VariableDefinitions: cloneVariableDefinitions(op.VariableDefinitions),
		Directives:          cloneDirectives(op.Directives),
		SelectionSet:        cloneSelectionSet(op.SelectionSet),
		Position:            op.Position,
		Comment:             op.Comment,
	}
}

func cloneFragmentDefinition(f *ast.FragmentDefinition) *ast.FragmentDefinition {
	return &ast.FragmentDefinition{
		Description:        f.Description,
		Name:               f.Name,
		VariableDefinition: cloneVariableDefinitions(f.VariableDefinition),
		TypeCondition:      f.TypeCondition,
		Directives:         cloneDirectives(f.Directives),
		SelectionSet:       cloneSelectionSet(f.SelectionSet),
		Definition:         nil,
		Position:           f.Position,
		Comment:            f.Comment,
	}
}

func cloneVariableDefinitions(
	in ast.VariableDefinitionList,
) ast.VariableDefinitionList {
	if len(in) == 0 {
		return nil
	}
	out := make(ast.VariableDefinitionList, 0, len(in))
	for _, v := range in {
		out = append(out, &ast.VariableDefinition{
			Description:  v.Description,
			Variable:     v.Variable,
			Type:         cloneType(v.Type),
			DefaultValue: cloneValue(v.DefaultValue),
			Directives:   cloneDirectives(v.Directives),
			Position:     v.Position,
			Comment:      v.Comment,
		})
	}
	return out
}

func cloneType(t *ast.Type) *ast.Type {
	if t == nil {
		return nil
	}
	return &ast.Type{
		NamedType: t.NamedType,
		Elem:      cloneType(t.Elem),
		NonNull:   t.NonNull,
		Position:  t.Position,
	}
}

func cloneSelectionSet(in ast.SelectionSet) ast.SelectionSet {
	if len(in) == 0 {
		return nil
	}
	out := make(ast.SelectionSet, 0, len(in))
	for _, sel := range in {
		switch sel := sel.(type) {
		case *ast.Field:
			out = append(out, &ast.Field{
				Alias:        sel.Alias,
				Name:         sel.Name,
				Arguments:    cloneArguments(sel.Arguments),
				Directives:   cloneDirectives(sel.Directives),
				SelectionSet: cloneSelectionSet(sel.SelectionSet),
				Position:     sel.Position,
				Comment:      sel.Comment,
			})
		case *ast.FragmentSpread:
			out = append(out, &ast.FragmentSpread{
				Name:       sel.Name,
				Directives: cloneDirectives(sel.Directives),
				Position:   sel.Position,
				Comment:    sel.Comment,
			})
		case *ast.InlineFragment:
			out = append(out, &ast.InlineFragment{
				TypeCondition: sel.TypeCondition,
				Directives:    cloneDirectives(sel.Directives),
				SelectionSet:  cloneSelectionSet(sel.SelectionSet),
				Position:      sel.Position,
				Comment:       sel.Comment,
			})
		}
	}
	return out
}

func cloneDirectives(in ast.DirectiveList) ast.DirectiveList {
	if len(in) == 0 {
		return nil
	}
	out := make(ast.DirectiveList, 0, len(in))
	for _, d := range in {
		out = append(out, &ast.Directive{
			Name:      d.Name,
			Arguments: cloneArguments(d.Arguments),
			Position:  d.Position,
		})
	}
	return out
}

func cloneArguments(in ast.ArgumentList) ast.ArgumentList {
	if len(in) == 0 {
		return nil
	}
	out := make(ast.ArgumentList, 0, len(in))
	for _, a := range in {
		out = append(out, &ast.Argument{
			Name:     a.Name,
			Value:    cloneValue(a.Value),
			Position: a.Position,
			Comment:  a.Comment,
		})
	}
	return out
}

func cloneValue(v *ast.Value) *ast.Value {
	if v == nil {
		return nil
	}
	out := &ast.Value{
		Raw:      v.Raw,
		Kind:     v.Kind,
		Position: v.Position,
		Comment:  v.Comment,
	}
	if len(v.Children) > 0 {
		out.Children = make(ast.ChildValueList, 0, len(v.Children))
		for _, c := range v.Children {
			out.Children = append(out.Children, &ast.ChildValue{
				Name:     c.Name,
				Value:    cloneValue(c.Value),
				Position: c.Position,
				Comment:  c.Comment,
			})
		}
	}
	return out
}
