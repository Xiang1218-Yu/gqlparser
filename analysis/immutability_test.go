package analysis_test

import (
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// snapshotMutatedState records every AST field the validator walker normally
// annotates. Analysis must leave them all at their parsed zero values on the
// caller's document.
func snapshotMutatedState(doc *ast.QueryDocument) string {
	var b strings.Builder

	mark := func(ok bool, label string) {
		if ok {
			b.WriteString("!")
			b.WriteString(label)
			b.WriteString(";")
		}
	}

	var walkValue func(prefix string, v *ast.Value)
	var walkArgs func(prefix string, args ast.ArgumentList)
	var walkDirectives func(prefix string, dirs ast.DirectiveList)
	var walkSels func(prefix string, sels ast.SelectionSet)

	walkValue = func(prefix string, v *ast.Value) {
		if v == nil {
			return
		}
		mark(v.VariableDefinition != nil, prefix+".varDef")
		mark(v.Definition != nil, prefix+".def")
		mark(v.ExpectedType != nil, prefix+".expected")
		for _, c := range v.Children {
			walkValue(prefix+"."+c.Name, c.Value)
		}
	}
	walkArgs = func(prefix string, args ast.ArgumentList) {
		for _, a := range args {
			walkValue(prefix+"."+a.Name, a.Value)
		}
	}
	walkDirectives = func(prefix string, dirs ast.DirectiveList) {
		for _, d := range dirs {
			p := prefix + ".dir" + d.Name
			mark(d.Definition != nil, p+".def")
			mark(d.ParentDefinition != nil, p+".parent")
			mark(d.Location != "", p+".loc")
			walkArgs(p, d.Arguments)
		}
	}
	walkSels = func(prefix string, sels ast.SelectionSet) {
		for _, sel := range sels {
			switch s := sel.(type) {
			case *ast.Field:
				p := prefix + "/" + s.Name
				mark(s.Definition != nil, p+".def")
				mark(s.ObjectDefinition != nil, p+".obj")
				walkArgs(p, s.Arguments)
				walkDirectives(p, s.Directives)
				walkSels(p, s.SelectionSet)
			case *ast.FragmentSpread:
				p := prefix + "/..." + s.Name
				mark(s.Definition != nil, p+".def")
				mark(s.ObjectDefinition != nil, p+".obj")
				walkDirectives(p, s.Directives)
			case *ast.InlineFragment:
				p := prefix + "/...on" + s.TypeCondition
				mark(s.ObjectDefinition != nil, p+".obj")
				walkDirectives(p, s.Directives)
				walkSels(p, s.SelectionSet)
			}
		}
	}

	for _, op := range doc.Operations {
		p := "op" + op.Name
		for _, v := range op.VariableDefinitions {
			mark(v.Definition != nil, p+".$"+v.Variable+".def")
			mark(v.Used, p+".$"+v.Variable+".used")
			walkValue(p+".$"+v.Variable, v.DefaultValue)
			walkDirectives(p+".$"+v.Variable, v.Directives)
		}
		walkDirectives(p, op.Directives)
		walkSels(p, op.SelectionSet)
	}
	for _, f := range doc.Fragments {
		p := "frag" + f.Name
		mark(f.Definition != nil, p+".def")
		walkDirectives(p, f.Directives)
		walkSels(p, f.SelectionSet)
	}

	return b.String()
}
