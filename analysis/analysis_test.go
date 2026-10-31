package analysis_test

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vektah/gqlparser/v2/analysis"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
)

const testSDL = `
directive @trace on QUERY

interface Node { id: ID! }

interface Character {
  id: ID!
  name: String!
  friends: [Character!]
}

type Human implements Character & Node {
  id: ID!
  name: String!
  friends: [Character!]
  homePlanet: String
}

type Droid implements Character {
  id: ID!
  name: String!
  friends: [Character!]
  primaryFunction: String
}

enum Episode { NEWHOPE EMPIRE JEDI }

input Filter { take: Int, skip: Int }

type Query {
  hero(episode: Episode): Character
  human(id: ID!): Human
  droid(id: ID!): Droid
  characters: [Character!]!
  search(filter: Filter!): [Character!]!
  count: Int!
}

type Mutation {
  renameHuman(id: ID!, name: String!): Human
}
`

func mustSchema(t *testing.T, sdl string) *ast.Schema {
	t.Helper()
	source := &ast.Source{Name: "schema.graphql", Input: sdl}
	schema, err := validator.LoadSchema(append([]*ast.Source{validator.Prelude}, source)...)
	require.NoError(t, err)
	return schema
}

func mustParse(t *testing.T, query string) *ast.QueryDocument {
	t.Helper()
	doc, err := parser.ParseQuery(&ast.Source{Name: "query.graphql", Input: query})
	require.NoError(t, err)
	return doc
}

func analyze(
	t *testing.T,
	schema *ast.Schema,
	query, name string,
	opts *analysis.Options,
) *analysis.Snapshot {
	t.Helper()
	doc := mustParse(t, query)
	snap, errs := analysis.Analyze(schema, doc, name, opts)
	require.Nil(t, errs, "expected no analysis errors: %v", errs)
	require.NotNil(t, snap)
	return snap
}

func analyzeExpectError(t *testing.T, schema *ast.Schema, query, name string) analysis.ErrorList {
	t.Helper()
	doc := mustParse(t, query)
	snap, errs := analysis.Analyze(schema, doc, name, nil)
	require.NotNil(t, errs)
	require.Nil(t, snap, "failed analysis must not retain a partial snapshot")
	return errs
}

func findField(t *testing.T, fields []*analysis.Field, path ...string) *analysis.Field {
	t.Helper()
	var current *analysis.Field
	remaining := fields
	for _, segment := range path {
		var next *analysis.Field
		for _, f := range remaining {
			if f.ResponseName == segment {
				next = f
				break
			}
		}
		require.NotNil(t, next, "field %q not found (looking for %v)", segment, path)
		current = next
		remaining = next.Fields
	}
	return current
}

func findFragment(snap *analysis.Snapshot, name string) *analysis.Fragment {
	for i := range snap.Fragments {
		if snap.Fragments[i].Name == name {
			return &snap.Fragments[i]
		}
	}
	return nil
}

func TestAnalyzeBasicOperation(t *testing.T) {
	schema := mustSchema(t, testSDL)
	query := `
		query GetHero($id: ID!, $ep: Episode = NEWHOPE) @trace {
			hero(episode: $ep) {
				__typename
				id
				name
				friends @include(if: true) { id name }
			}
			human(id: $id) { name homePlanet }
		}`
	snap := analyze(t, schema, query, "GetHero", nil)

	require.Equal(t, "GetHero", snap.Operation)
	require.Equal(t, "query", snap.OperationType)
	require.Equal(t, "Query", snap.RootType)
	require.Len(t, snap.Variables, 2)
	require.Equal(t, "id", snap.Variables[0].Name)
	require.Equal(t, "ID!", snap.Variables[0].Type)
	require.True(t, snap.Variables[0].Required)
	require.False(t, snap.Variables[0].HasDefault)
	require.Equal(t, "ep", snap.Variables[1].Name)
	require.False(t, snap.Variables[1].Required)
	require.True(t, snap.Variables[1].HasDefault)
	require.Equal(t, "NEWHOPE", snap.Variables[1].Default)

	require.Len(t, snap.Directives, 1)
	require.Equal(t, "trace", snap.Directives[0].Name)

	// Sorted root fields.
	require.Len(t, snap.Fields, 2)
	require.Equal(t, "hero", snap.Fields[0].ResponseName)
	require.Equal(t, "human", snap.Fields[1].ResponseName)

	hero := snap.Fields[0]
	require.Equal(t, "Character", hero.Type)
	require.Len(t, hero.Arguments, 1)
	require.Equal(t, analysis.Argument{Name: "episode", Value: "$ep"}, hero.Arguments[0])
	require.Empty(t, hero.Alias)
	require.Equal(t, []string{"hero"}, hero.ResponsePath)
	require.NotEmpty(t, hero.NodeID)
	require.Len(t, hero.Locations, 1)
	require.Positive(t, hero.Locations[0].Line)
	require.Positive(t, hero.Locations[0].Column)
	require.Equal(t, "query.graphql", hero.Locations[0].Source)

	friends := findField(t, snap.Fields, "hero", "friends")
	require.Equal(t, "[Character!]", friends.Type)
	require.Len(t, friends.Directives, 1)
	require.Equal(t, "include", friends.Directives[0].Name)

	typename := findField(t, snap.Fields, "hero", "__typename")
	require.Equal(t, "String", typename.Type)

	// Distinct paths get distinct ids.
	nameUnderHero := findField(t, snap.Fields, "hero", "name")
	nameUnderHuman := findField(t, snap.Fields, "human", "name")
	require.NotEqual(t, nameUnderHero.NodeID, nameUnderHuman.NodeID)
	require.NotEqual(t, nameUnderHero.NodeID, friends.NodeID)

	// hero branch 7 paths, human branch 3, merged by response path.
	require.Equal(t, 10, snap.FieldCount)
	require.Equal(t, 3, snap.MaxDepth)

	require.Contains(t, snap.Summary, "query GetHero")
	require.Contains(t, snap.Summary, "root Query")
	require.Contains(t, snap.Summary, "$ep: Episode = NEWHOPE")
	require.Contains(t, snap.Summary, "fields 10 depth 3")
}

func TestAnalyzeAnonymousShorthand(t *testing.T) {
	schema := mustSchema(t, testSDL)
	snap := analyze(t, schema, `{ count }`, "", nil)
	require.Equal(t, "", snap.Operation)
	require.Equal(t, "query", snap.OperationType)
	require.Equal(t, 1, snap.FieldCount)
	require.Equal(t, 1, snap.MaxDepth)
}

func TestAnalyzeFragments(t *testing.T) {
	schema := mustSchema(t, testSDL)
	// Fragment definitions are deliberately out of reference order.
	query := `
		query Q {
			hero {
				...CharFields
				... on Human { homePlanet }
				... on Droid { primaryFunction }
			}
			...RootFrag
		}
		fragment FName on Character { name }
		fragment RootFrag on Query { human(id: "1") { ...CharFields } }
		fragment CharFields on Character {
			id
			name
			friends { ...FName }
		}`
	snap := analyze(t, schema, query, "Q", nil)

	// First-reference order, independent of definition order.
	require.Equal(t, []string{"CharFields", "FName", "RootFrag"}, fragmentNames(snap))

	charFields := findFragment(snap, "CharFields")
	require.Equal(t, "Character", charFields.TypeCondition)
	// Spread once under hero and once again inside RootFrag under human.
	require.Len(t, charFields.Spreads, 2)
	require.Equal(t, []string{"hero"}, charFields.Spreads[0].Path)
	require.Equal(t, []string{"human"}, charFields.Spreads[1].Path)

	rootFrag := findFragment(snap, "RootFrag")
	require.Equal(t, "Query", rootFrag.TypeCondition)
	require.Len(t, rootFrag.Spreads, 1)
	require.Nil(t, rootFrag.Spreads[0].Path)

	// name under CharFields, reached directly and through friends/FName,
	// merges per response path. Conditions matching the parent type are not
	// restrictions, so no type conditions are recorded here.
	nameUnderHero := findField(t, snap.Fields, "hero", "name")
	require.Empty(t, nameUnderHero.TypeConditions)
	nameUnderFriends := findField(t, snap.Fields, "hero", "friends", "name")
	require.Empty(t, nameUnderFriends.TypeConditions)

	home := findField(t, snap.Fields, "hero", "homePlanet")
	require.Equal(t, []string{"Human"}, home.TypeConditions)
	funcField := findField(t, snap.Fields, "hero", "primaryFunction")
	require.Equal(t, []string{"Droid"}, funcField.TypeConditions)

	// human reached through RootFrag on Query (no narrowing) and CharFields on
	// Character (narrows the concrete Human), so Character is recorded.
	humanName := findField(t, snap.Fields, "human", "name")
	require.Equal(t, []string{"Character"}, humanName.TypeConditions)
}

func TestAnalyzeMergesRepeatedAndDuplicateReferences(t *testing.T) {
	schema := mustSchema(t, testSDL)

	// Same fragment spread twice at the same path: fields merge.
	snap := analyze(t, schema, `
		query Q { hero { ...A ...A } }
		fragment A on Character { name }
	`, "Q", nil)
	name := findField(t, snap.Fields, "hero", "name")
	require.Len(t, name.Locations, 1) // same AST occurrence, de-duplicated
	require.Equal(t, 2, snap.FieldCount)

	// Two different fragments selecting the same field: compatible merge with
	// two source locations.
	snap = analyze(t, schema, `
		query Q { hero { ...A ...B } }
		fragment A on Character { name }
		fragment B on Character { name }
	`, "Q", nil)
	name = findField(t, snap.Fields, "hero", "name")
	require.Len(t, name.Locations, 2)
	// Both fragments match the parent type Character, so they add no narrowing
	// condition.
	require.Empty(t, name.TypeConditions)
	require.Equal(t, 2, snap.FieldCount)
}

func TestAnalyzeNestedFragmentCycleTerminatesMeasurement(t *testing.T) {
	schema := mustSchema(t, testSDL)
	errs := analyzeExpectError(t, schema, `
		query Q { hero { ...A } }
		fragment A on Character { ...B }
		fragment B on Character { ...A }
	`, "Q")
	requireKind(t, errs, analysis.ErrFragmentCycle)
}

// ---------------------------------------------------------------------------
// Structured errors.
// ---------------------------------------------------------------------------

func TestAnalyzeErrorAliasConflict(t *testing.T) {
	schema := mustSchema(t, testSDL)
	errs := analyzeExpectError(t, schema, `
		query Q { hero { id } hero: human(id: "1") { id } }
	`, "Q")
	e := requireKind(t, errs, analysis.ErrAliasConflict)
	require.Equal(t, "Q", e.Operation)
	require.Equal(t, []string{"hero"}, e.Path)
	require.Positive(t, e.Locations[0].Line)
}

func TestAnalyzeErrorArgumentConflict(t *testing.T) {
	schema := mustSchema(t, testSDL)
	errs := analyzeExpectError(t, schema, `
		query Q {
			hero(episode: NEWHOPE) { id }
			hero { id }
		}
	`, "Q")
	requireKind(t, errs, analysis.ErrArgumentConflict)
}

func TestAnalyzeErrorUndefinedVariable(t *testing.T) {
	schema := mustSchema(t, testSDL)
	errs := analyzeExpectError(t, schema, `
		query Q { human(id: $missing) { id } }
	`, "Q")
	e := requireKind(t, errs, analysis.ErrUndefinedVariable)
	require.Equal(t, []string{"human"}, e.Path)
	require.Contains(t, e.Message, "$missing")
}

func TestAnalyzeErrorUnknownFragment(t *testing.T) {
	schema := mustSchema(t, testSDL)
	errs := analyzeExpectError(t, schema, `
		query Q { hero { ...Nope } }
	`, "Q")
	e := requireKind(t, errs, analysis.ErrUnknownFragment)
	require.Equal(t, []string{"hero"}, e.Path)
}

func TestAnalyzeErrorAnonymousNotUnique(t *testing.T) {
	schema := mustSchema(t, testSDL)
	doc := mustParse(t, `{ count } { count }`)
	snap, errs := analysis.Analyze(schema, doc, "", nil)
	require.Nil(t, snap)
	require.Len(t, errs, 2)
	for _, e := range errs {
		require.Equal(t, analysis.ErrAnonymousNotUnique, e.Kind)
	}
}

func TestAnalyzeErrorFieldNotFound(t *testing.T) {
	schema := mustSchema(t, testSDL)
	errs := analyzeExpectError(t, schema, `
		query Q { hero { nonsense } }
	`, "Q")
	e := requireKind(t, errs, analysis.ErrFieldNotFound)
	require.Equal(t, []string{"hero", "nonsense"}, e.Path)
}

func TestAnalyzeErrorScalarSelection(t *testing.T) {
	schema := mustSchema(t, testSDL)
	errs := analyzeExpectError(t, schema, `query Q { count { x } }`, "Q")
	e := requireKind(t, errs, analysis.ErrSelectionTypeMismatch)
	require.Equal(t, []string{"count"}, e.Path)
}

func TestAnalyzeErrorCompositeWithoutSelection(t *testing.T) {
	schema := mustSchema(t, testSDL)
	errs := analyzeExpectError(t, schema, `query Q { hero }`, "Q")
	e := requireKind(t, errs, analysis.ErrSelectionTypeMismatch)
	require.Equal(t, []string{"hero"}, e.Path)
}

func TestAnalyzeErrorImpossibleFragmentSpread(t *testing.T) {
	schema := mustSchema(t, testSDL)
	errs := analyzeExpectError(t, schema, `
		fragment HumanFrag on Human { id }
		query Q { droid(id: "1") { ...HumanFrag } }
	`, "Q")
	e := requireKind(t, errs, analysis.ErrSelectionTypeMismatch)
	require.Equal(t, []string{"droid"}, e.Path)
}

func TestAnalyzeErrorRootOperationMissing(t *testing.T) {
	queryOnly := `
		type Query { count: Int! }
	`
	schema := mustSchema(t, queryOnly)
	errs := analyzeExpectError(t, schema, `mutation M { count }`, "M")
	e := requireKind(t, errs, analysis.ErrRootOperationMissing)
	require.Equal(t, "M", e.Operation)
}

func TestAnalyzeErrorOperationNotFound(t *testing.T) {
	schema := mustSchema(t, testSDL)
	doc := mustParse(t, `query Q { count }`)

	_, errs := analysis.Analyze(schema, doc, "Nope", nil)
	require.Len(t, errs, 1)
	require.Equal(t, analysis.ErrOperationNotFound, errs[0].Kind)

	doc = mustParse(t, `query A { count } query B { count }`)
	_, errs = analysis.Analyze(schema, doc, "", nil)
	require.Equal(t, analysis.ErrOperationNotFound, errs[0].Kind)

	doc = mustParse(t, `fragment F on Query { count }`)
	_, errs = analysis.Analyze(schema, doc, "", nil)
	require.Equal(t, analysis.ErrOperationNotFound, errs[0].Kind)
}

func TestAnalyzeErrorsAreSortedAndComplete(t *testing.T) {
	schema := mustSchema(t, testSDL)
	errs := analyzeExpectError(t, schema, `
		query Q {
			hero { nonsense1 }
			count { x }
		}
	`, "Q")
	require.GreaterOrEqual(t, len(errs), 2)
	for i := 1; i < len(errs); i++ {
		prev, cur := errs[i-1].Locations[0], errs[i].Locations[0]
		require.True(t,
			prev.Line < cur.Line || prev.Line == cur.Line && prev.Column <= cur.Column,
			"errors not sorted: %+v before %+v", prev, cur)
	}
}

// ---------------------------------------------------------------------------
// Limits.
// ---------------------------------------------------------------------------

func TestAnalyzeLimitsEnforced(t *testing.T) {
	schema := mustSchema(t, testSDL)

	_, errs := analysis.Analyze(schema, mustParse(t, `query Q { hero { id name } }`),
		"Q", &analysis.Options{MaxFields: 2})
	require.Len(t, errs, 1)
	require.Equal(t, analysis.ErrMaxFieldsExceeded, errs[0].Kind)
	require.Contains(t, errs[0].Message, "maximum is 2")

	_, errs = analysis.Analyze(schema, mustParse(t, `query Q { hero { id } }`),
		"Q", &analysis.Options{MaxDepth: 1})
	require.Len(t, errs, 1)
	require.Equal(t, analysis.ErrMaxDepthExceeded, errs[0].Kind)

	// Within the limits analysis succeeds.
	snap := analyze(t, schema, `query Q { hero { id name } }`, "Q",
		&analysis.Options{MaxFields: 3, MaxDepth: 2})
	require.Equal(t, 3, snap.FieldCount)
}

func TestAnalyzeLimitsCheckedBeforeValidation(t *testing.T) {
	schema := mustSchema(t, testSDL)
	// Both over-depth and invalid: the limit error must come first and be the
	// only result, with no partial snapshot.
	doc := mustParse(t, `query Q { hero { hero2 { hero3 { id } } } }`)
	snap, errs := analysis.Analyze(schema, doc, "Q", &analysis.Options{MaxDepth: 2})
	require.Nil(t, snap)
	require.Len(t, errs, 1)
	require.Equal(t, analysis.ErrMaxDepthExceeded, errs[0].Kind)
}

func TestAnalyzeInvalidOptionsAndInputs(t *testing.T) {
	schema := mustSchema(t, testSDL)
	doc := mustParse(t, `{ count }`)

	_, errs := analysis.Analyze(nil, doc, "", nil)
	requireKind(t, errs, analysis.ErrInvalidInput)

	_, errs = analysis.Analyze(schema, nil, "", nil)
	requireKind(t, errs, analysis.ErrInvalidInput)

	_, errs = analysis.Analyze(schema, doc, "", &analysis.Options{MaxFields: -1})
	requireKind(t, errs, analysis.ErrInvalidInput)
}

// ---------------------------------------------------------------------------
// Stability and immutability.
// ---------------------------------------------------------------------------

func TestAnalyzeRepeatedCallsAreIdentical(t *testing.T) {
	schema := mustSchema(t, testSDL)
	query := `query Q { hero { id name friends { id } } }`
	doc := mustParse(t, query)

	first, errs := analysis.Analyze(schema, doc, "Q", nil)
	require.Nil(t, errs)
	second, errs := analysis.Analyze(schema, doc, "Q", nil)
	require.Nil(t, errs)
	require.True(t, reflect.DeepEqual(first, second),
		"repeated analysis must be identical")
}

func TestAnalyzeStableAcrossFormattingAndFragmentOrder(t *testing.T) {
	schema := mustSchema(t, testSDL)

	queries := []string{
		`query Q { hero { ...F ...G } }
		 fragment F on Character { name }
		 fragment G on Character { id }`,
		`# leading comment
		 query   Q {
			hero { # inline comment
				...F
				...G
			}
		 }
		 fragment G on Character { id }
		 fragment F on Character { name }`,
	}

	first := analyze(t, schema, queries[0], "Q", nil)
	second := analyze(t, schema, queries[1], "Q", nil)

	require.Equal(t, stripLocations(first), stripLocations(second))
	require.Equal(t, first.Summary, second.Summary)
	require.Equal(t, first.FieldCount, second.FieldCount)
	require.Equal(t, first.MaxDepth, second.MaxDepth)
}

func TestAnalyzeStableAcrossInputObjectFieldOrder(t *testing.T) {
	schema := mustSchema(t, testSDL)
	v1 := analyze(t, schema, `
		query Q {
			a: search(filter: {take: 2, skip: 1}) { name }
			b: search(filter: {skip: 1, take: 2}) { name }
		}`, "Q", nil)
	v2 := analyze(t, schema, `
		query Q {
			a: search(filter: {skip: 1, take: 2}) { name }
			b: search(filter: {take: 2, skip: 1}) { name }
		}`, "Q", nil)

	a := findField(t, v1.Fields, "a")
	require.Equal(t, "filter", a.Arguments[0].Name)
	require.Equal(t, "{skip:1,take:2}", a.Arguments[0].Value)
	require.Equal(t, stripLocations(v1), stripLocations(v2))
}

func TestAnalyzeAliases(t *testing.T) {
	schema := mustSchema(t, testSDL)
	snap := analyze(t, schema, `
		query Q {
			a: search(filter: {take: 1}) { name }
			b: search(filter: {take: 2}) { name }
		}
	`, "Q", nil)

	a := findField(t, snap.Fields, "a")
	require.Equal(t, "search", a.Name)
	require.Equal(t, "a", a.Alias)
	require.Equal(t, "a", a.ResponseName)
	require.Equal(t, []string{"a"}, a.ResponsePath)

	b := findField(t, snap.Fields, "b")
	require.Equal(t, "search", b.Name)
	require.Equal(t, "b", b.Alias)
	require.NotEqual(t, a.NodeID, b.NodeID)
}

func TestAnalyzeSubscription(t *testing.T) {
	schema := mustSchema(t, `
		type Query { count: Int! }
		type Subscription { count: Int! }
	`)
	snap := analyze(t, schema, `subscription S { count }`, "S", nil)
	require.Equal(t, "subscription", snap.OperationType)
	require.Equal(t, "Subscription", snap.RootType)
}

func TestAnalyzeScopesErrorsToSelectedOperation(t *testing.T) {
	schema := mustSchema(t, testSDL)
	// One valid operation, one invalid sibling operation, and an unused
	// fragment that full document validation rejects.
	doc := mustParse(t, `
		query Good { count }
		query Bad { hero { nonsense } }
		fragment Unused on Query { count }
	`)

	snap, errs := analysis.Analyze(schema, doc, "Good", nil)
	require.Nil(t, errs, "valid operation must not inherit sibling/fragment errors: %v", errs)
	require.NotNil(t, snap)
	require.Equal(t, "Good", snap.Operation)

	snap, errs = analysis.Analyze(schema, doc, "Bad", nil)
	require.Nil(t, snap)
	requireKind(t, errs, analysis.ErrFieldNotFound)
}

func TestAnalyzeStableDefaultValueWithObject(t *testing.T) {
	schema := mustSchema(t, `
		input Review { stars: Int!, note: String }
		type Query { count: Int!, submit(review: Review): Int! }
	`)
	snap := analyze(t, schema, `
		query Q($r: Review = {note: "ok", stars: 5}) { submit(review: $r) }
	`, "Q", nil)
	require.Equal(t, `{note:"ok",stars:5}`, snap.Variables[0].Default)
}

func TestAnalyzeConcurrent(t *testing.T) {
	schema := mustSchema(t, testSDL)
	doc := mustParse(t, `query Q { hero { id name friends { id } } }`)

	first, errs := analysis.Analyze(schema, doc, "Q", nil)
	require.Nil(t, errs)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap, errs := analysis.Analyze(schema, doc, "Q",
				&analysis.Options{MaxFields: 100, MaxDepth: 10})
			require.Nil(t, errs)
			require.True(t, reflect.DeepEqual(first, snap))
		}()
	}
	wg.Wait()
}

func TestAnalyzeDoesNotMutateInputs(t *testing.T) {
	schema := mustSchema(t, testSDL)
	doc := mustParse(t, `
		query Q($id: ID!) {
			hero @include(if: true) { id friends { name } }
			human(id: $id) { ...F }
		}
		fragment F on Human { name }
	`)

	before := snapshotMutatedState(doc)
	_, errs := analysis.Analyze(schema, doc, "Q", nil)
	require.Nil(t, errs)
	_, errs = analysis.Analyze(schema, doc, "Q", nil)
	require.Nil(t, errs)
	after := snapshotMutatedState(doc)

	require.Equal(t, before, after,
		"analysis must not annotate the caller's AST")
}

func TestAnalyzeDoesNotMutateOptions(t *testing.T) {
	schema := mustSchema(t, testSDL)
	opts := &analysis.Options{MaxFields: 100, MaxDepth: 10}
	analyze(t, schema, `query Q { count }`, "Q", opts)
	require.Equal(t, &analysis.Options{MaxFields: 100, MaxDepth: 10}, opts)
}

func TestSnapshotIsImmutableAndSelfContained(t *testing.T) {
	schema := mustSchema(t, testSDL)
	snap := analyze(t, schema, `
		query Q($ep: Episode = JEDI) { hero(episode: $ep) { name } }
	`, "Q", nil)

	snap.Fields[0].ResponseName = "mutated"
	snap.Variables[0].Default = "mutated"

	again := analyze(
		t,
		schema,
		`query Q($ep: Episode = JEDI) { hero(episode: $ep) { name } }`,
		"Q",
		nil,
	)
	require.Equal(t, "hero", again.Fields[0].ResponseName)
	require.Equal(t, "JEDI", again.Variables[0].Default)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func requireKind(t *testing.T, errs analysis.ErrorList, kind analysis.ErrorKind) *analysis.Error {
	t.Helper()
	for _, e := range errs {
		if e.Kind == kind {
			return e
		}
	}
	var kinds []string
	for _, e := range errs {
		kinds = append(kinds, string(e.Kind))
	}
	require.Failf(t, "missing expected error kind", "wanted %s, got %v (%v)",
		kind, kinds, errs)
	return nil
}

func fragmentNames(snap *analysis.Snapshot) []string {
	names := make([]string, 0, len(snap.Fragments))
	for _, f := range snap.Fragments {
		names = append(names, f.Name)
	}
	return names
}

// stripLocations renders a snapshot with every source position cleared so two
// snapshots that differ only in whitespace can be compared for shape, ids and
// summary stability.
func stripLocations(snap *analysis.Snapshot) string {
	var b strings.Builder
	var walk func(fields []*analysis.Field, indent string)
	walk = func(fields []*analysis.Field, indent string) {
		for _, f := range fields {
			fmt.Fprintf(&b, "%s%s:%s id=%s conds=%v\n",
				indent, f.ResponseName, f.Type, f.NodeID, f.TypeConditions)
			walk(f.Fields, indent+"  ")
		}
	}
	fmt.Fprintf(&b, "%s %s root=%s fields=%d depth=%d vars=%v frags=%v\n",
		snap.OperationType, snap.Operation, snap.RootType,
		snap.FieldCount, snap.MaxDepth, variableDefaults(snap), fragmentNames(snap))
	walk(snap.Fields, "")
	return b.String()
}

func variableDefaults(snap *analysis.Snapshot) []string {
	out := make([]string, 0, len(snap.Variables))
	for _, v := range snap.Variables {
		out = append(out, v.Name+"="+v.Default)
	}
	return out
}
