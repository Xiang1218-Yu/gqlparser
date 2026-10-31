package gqlparser

import (
	"errors"

	"github.com/vektah/gqlparser/v2/analysis"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
)

// AnalyzeOperation produces an immutable analysis snapshot of one operation in
// an already parsed and schema-valid query document. The capability is
// opt-in: it changes nothing when it is not called. See [analysis.Analyze] for
// the semantics.
func AnalyzeOperation(
	schema *ast.Schema,
	query *ast.QueryDocument,
	operationName string,
	opts *analysis.Options,
) (*analysis.Snapshot, analysis.ErrorList) {
	return analysis.Analyze(schema, query, operationName, opts)
}

// AnalyzeQuery parses query using gqlparser's parser and, when parsing
// succeeds, analyzes the named operation. Parse failures come back as
// structured analysis errors with kind [analysis.ErrParseError]; the input
// document is otherwise treated exactly like [AnalyzeOperation].
func AnalyzeQuery(
	schema *ast.Schema,
	query string,
	operationName string,
	opts *analysis.Options,
) (*analysis.Snapshot, analysis.ErrorList) {
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil {
		return nil, analysis.ErrorList{toAnalysisError(err)}
	}
	return analysis.Analyze(schema, doc, operationName, opts)
}

func toAnalysisError(err error) *analysis.Error {
	gqlErr := &gqlerror.Error{}
	if !errors.As(err, &gqlErr) {
		return &analysis.Error{
			Kind:    analysis.ErrParseError,
			Message: err.Error(),
		}
	}
	locs := make([]analysis.Location, 0, len(gqlErr.Locations))
	for _, l := range gqlErr.Locations {
		file, _ := gqlErr.Extensions["file"].(string)
		locs = append(locs, analysis.Location{
			Line:   l.Line,
			Column: l.Column,
			Source: file,
		})
	}
	return &analysis.Error{
		Kind:      analysis.ErrParseError,
		Message:   gqlErr.Message,
		Locations: locs,
	}
}
