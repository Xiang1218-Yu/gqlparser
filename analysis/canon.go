package analysis

import (
	"slices"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

func locationOf(pos *ast.Position) Location {
	if pos == nil {
		return Location{}
	}
	src := ""
	if pos.Src != nil {
		src = pos.Src.Name
	}
	return Location{Line: pos.Line, Column: pos.Column, Source: src}
}

func locsOf(pos *ast.Position) []Location {
	if pos == nil || (pos.Line == 0 && pos.Column == 0) {
		return nil
	}
	return []Location{locationOf(pos)}
}

// canonicalValue renders an ast value in a form that depends only on its
// GraphQL content. Input object fields are sorted by name, so two literals
// that differ only in field order or in pointer reuse render identically.
func canonicalValue(v *ast.Value) string {
	if v == nil {
		return "null"
	}
	switch v.Kind {
	case ast.ObjectValue:
		children := slices.Clone(v.Children)
		slices.SortStableFunc(children, func(a, b *ast.ChildValue) int {
			return strings.Compare(a.Name, b.Name)
		})
		parts := make([]string, 0, len(children))
		for _, c := range children {
			parts = append(parts, c.Name+":"+canonicalValue(c.Value))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case ast.ListValue:
		parts := make([]string, 0, len(v.Children))
		for _, c := range v.Children {
			parts = append(parts, canonicalValue(c.Value))
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		return v.String()
	}
}

func argumentsOf(in ast.ArgumentList) []Argument {
	if len(in) == 0 {
		return nil
	}
	out := make([]Argument, 0, len(in))
	for _, a := range in {
		out = append(out, Argument{Name: a.Name, Value: canonicalValue(a.Value)})
	}
	slices.SortFunc(out, func(a, b Argument) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func directiveUsesOf(in ast.DirectiveList) []DirectiveUse {
	if len(in) == 0 {
		return nil
	}
	out := make([]DirectiveUse, 0, len(in))
	for _, d := range in {
		out = append(out, DirectiveUse{
			Name:      d.Name,
			Arguments: argumentsOf(d.Arguments),
			Locations: locsOf(d.Position),
		})
	}
	sortDirectiveUses(out)
	return out
}

func sortDirectiveUses(uses []DirectiveUse) {
	slices.SortFunc(uses, func(a, b DirectiveUse) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return compareLocations(firstLoc(a.Locations), firstLoc(b.Locations))
	})
}

func firstLoc(locs []Location) Location {
	if len(locs) == 0 {
		return Location{}
	}
	return locs[0]
}

func compareLocations(a, b Location) int {
	if c := strings.Compare(a.Source, b.Source); c != 0 {
		return c
	}
	if a.Line != b.Line {
		return a.Line - b.Line
	}
	return a.Column - b.Column
}

// joinPath appends segment to an existing canonical response path key.
// GraphQL response names match [_A-Za-z][_0-9A-Za-z]*, so a dot separator
// cannot collide with one.
func joinPath(parent, segment string) string {
	if parent == "" {
		return segment
	}
	return parent + "." + segment
}

func copyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	slices.Sort(out)
	return slices.Compact(out)
}

// dedupLocations removes identical locations while preserving stable order.
func dedupLocations(in []Location) []Location {
	if len(in) <= 1 {
		return in
	}
	out := make([]Location, 0, len(in))
	for _, loc := range in {
		if !slices.Contains(out, loc) {
			out = append(out, loc)
		}
	}
	slices.SortFunc(out, compareLocations)
	return out
}
