package gqlparser_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/analysis"
	"github.com/vektah/gqlparser/v2/ast"
)

const wrapperSDL = `
type Query {
  hero: Character
  count: Int!
}
type Character { id: ID! name: String! }
`

func schemaSource() *ast.Source {
	return &ast.Source{Name: "schema.graphql", Input: wrapperSDL}
}

func TestAnalyzeOperationWrapper(t *testing.T) {
	schema := gqlparser.MustLoadSchema(schemaSource())
	doc := gqlparser.MustLoadQuery(schema, `query Q { hero { name } }`)

	snap, errs := gqlparser.AnalyzeOperation(schema, doc, "Q", nil)
	require.Nil(t, errs)
	require.Equal(t, "query", snap.OperationType)
	require.Equal(t, 2, snap.FieldCount)
	require.Equal(t, 2, snap.MaxDepth)
}

func TestAnalyzeQueryWrapper(t *testing.T) {
	schema := gqlparser.MustLoadSchema(schemaSource())

	snap, errs := gqlparser.AnalyzeQuery(schema, `{ hero { name } }`, "", nil)
	require.Nil(t, errs)
	require.Equal(t, 2, snap.FieldCount)

	_, errs = gqlparser.AnalyzeQuery(schema, `{ hero { `, "", nil)
	require.Len(t, errs, 1)
	require.Equal(t, analysis.ErrParseError, errs[0].Kind)
	require.NotEmpty(t, errs[0].Locations)
}
