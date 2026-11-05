//go:build write

package issue_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// The transport replays a PUT after a 5xx, and a request marked Idempotent
// too, because by HTTP semantics sending one twice has the effect of sending
// it once. A server can process a write and then answer 503, so the replay is
// a second application of the same write, and the result says it happened
// once. Measured 2026-09-03 against an httptest server: 4 attempts at one
// PUT, and an edit the server applied twice reported at exit 0.
//
// That is safe while a PUT's body is set-semantic. Its `fields` half replaces
// values outright, which a second sending repeats exactly. Its `update` half
// is a list of operations Jira applies incrementally, and an operation that
// appends, like `comment: [{add: ...}]`, posts a second comment. Today the
// only `update` operations on a PUT are a label's add and remove, labels are
// a set, and the one appending operation in the tree travels by POST to
// /transitions, which is never replayed. Nothing said it had to stay that way.

// setSemanticOps is every `update` operation a replayed request may carry,
// with the reason applying it twice lands where applying it once does.
//
// Adding an entry is a claim about Jira, so it says why. An operation that
// cannot honestly be listed here does not belong on a PUT: send it by POST, or
// split it into its own request.
var setSemanticOps = map[string]string{
	"labels.add":    "labels are a set, so adding one already there changes nothing",
	"labels.remove": "removing a label already gone changes nothing",
}

// walkedPuts is every function that builds a PUT, and replayablePosts every
// one that marks a POST Idempotent. TestEveryReplayedRequestIsWalked holds the
// tree to both lists, so a new one fails until somebody has decided what it
// sends and added it here.
var (
	walkedPuts = []string{
		"resource/issue.Client.AssignRequest",
		"resource/issue.Client.CommentEditRequest",
		"resource/issue.Client.EditRequest",
	}
	// Reads sent as POST because their body says what to read. None carries
	// an `update` block, and a change to that is a change to these.
	replayablePosts = []string{
		"site.checkByParse",
		"site.countApproximately",
		"site.searchNoRows",
	}
)

// updateOps names every `update` operation in a request body as field.op.
func updateOps(t *testing.T, body []byte) []string {
	t.Helper()
	var payload struct {
		Update map[string][]map[string]json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("a request body is not the JSON an update block decodes from: %v\n%s", err, body)
	}
	var ops []string
	for field, list := range payload.Update {
		for _, op := range list {
			for name := range op {
				ops = append(ops, field+"."+name)
			}
		}
	}
	slices.Sort(ops)
	return ops
}

// unsafeOps is the operations in ops that setSemanticOps does not vouch for.
func unsafeOps(ops []string) []string {
	var out []string
	for _, op := range ops {
		if _, ok := setSemanticOps[op]; !ok {
			out = append(out, op)
		}
	}
	return out
}

// fillEvery sets every field of an options struct to a value that asks for
// something, so an option added later is walked without anybody listing it.
// A field of a kind it cannot fill fails the test rather than being left
// empty: an option nobody sets is an option whose operations nobody checks.
func fillEvery(t *testing.T, v reflect.Value) {
	t.Helper()
	typ := v.Type()
	for i := range v.NumField() {
		f, name := v.Field(i), typ.Field(i).Name
		switch {
		case f.Kind() == reflect.String:
			// Two of them are issue keys and refuse anything else.
			value := map[string]string{"Key": "ENG-1", "Parent": "ENG-2"}[name]
			if value == "" {
				value = "x"
			}
			f.SetString(value)
		case f.Kind() == reflect.Bool:
			f.SetBool(true)
		case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.String:
			f.Set(reflect.ValueOf([]string{"x"}))
		case f.Kind() == reflect.Map && f.Type().Key().Kind() == reflect.String:
			m := reflect.MakeMap(f.Type())
			m.SetMapIndex(reflect.ValueOf("customfield_10000"), reflect.ValueOf("x"))
			f.Set(m)
		default:
			t.Fatalf("%s.%s is a %s, which this walk cannot fill. Teach fillEvery "+
				"to set it, so whatever it sends is checked", typ.Name(), name, f.Type())
		}
	}
}

// TestNoReplayedRequestCarriesAnAppendingUpdate is the invariant: every PUT
// this tree can build carries only `update` operations that are safe to apply
// twice. Each builder is driven on both deployments with every option it has
// set, so an edit flag added later reaches this test without being listed.
func TestNoReplayedRequestCarriesAnAppendingUpdate(t *testing.T) {
	var opt issue.EditOptions
	fillEvery(t, reflect.ValueOf(&opt).Elem())

	var seen []string
	for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
		client := &issue.Client{Site: site.Info{Kind: kind}}
		build := map[string]func() (transport.Request, error){
			"resource/issue.Client.EditRequest": func() (transport.Request, error) {
				return client.EditRequest(opt)
			},
			"resource/issue.Client.AssignRequest": func() (transport.Request, error) {
				return client.AssignRequest("ENG-1", "ada")
			},
			"resource/issue.Client.CommentEditRequest": func() (transport.Request, error) {
				return client.CommentEditRequest("ENG-1", "10000", "a new body", "")
			},
		}
		if got := slices.Sorted(maps.Keys(build)); !slices.Equal(got, walkedPuts) {
			t.Fatalf("the walk drives %v and walkedPuts names %v", got, walkedPuts)
		}
		for name, fn := range build {
			req, err := fn()
			if err != nil {
				t.Fatalf("%s on %s: %v", name, kind, err)
			}
			if req.Method != transport.MethodPut {
				t.Errorf("%s on %s sends %s; it is listed as a PUT", name, kind, req.Method)
			}
			ops := updateOps(t, req.Body)
			seen = append(seen, ops...)
			if bad := unsafeOps(ops); len(bad) > 0 {
				t.Errorf("%s on %s sends %v on a PUT, which the transport replays "+
					"after a 5xx. An operation that is not safe to apply twice "+
					"posts twice on a bad afternoon and reports one success. "+
					"Send it by POST, or list it in setSemanticOps with the reason "+
					"it is safe", name, kind, bad)
			}
		}
	}

	// A walk that found no operations at all passes whatever is sent.
	if !slices.Contains(seen, "labels.add") || !slices.Contains(seen, "labels.remove") {
		t.Errorf("the walk saw %v and not the label operations an edit sends, "+
			"so it is checking nothing", seen)
	}
}

// TestTheUpdateCheckFindsAnAppendingOperation is the control: the operation
// the invariant exists for is found, in the shape Jira takes it.
func TestTheUpdateCheckFindsAnAppendingOperation(t *testing.T) {
	body := []byte(`{"fields":{"summary":"x"},"update":{` +
		`"labels":[{"add":"a"}],"comment":[{"add":{"body":"posted twice"}}]}}`)
	if got := unsafeOps(updateOps(t, body)); !slices.Equal(got, []string{"comment.add"}) {
		t.Errorf("unsafe operations = %v, want [comment.add]", got)
	}
}

// TestEveryReplayedRequestIsWalked keeps the walk above whole. It reads the
// source under internal/ for every function that builds a PUT, by
// `transport.MethodPut` or the bare string, and every one that marks a request
// Idempotent, and holds each set to its list.
//
// The transport package is skipped: it holds the method table and the
// constant, and builds no request of its own.
func TestEveryReplayedRequestIsWalked(t *testing.T) {
	root := filepath.Join("..", "..")
	puts, posts := map[string]bool{}, map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			// The root is named "..", which the hidden-directory rule below
			// would skip, and with it everything.
			if rel == "." {
				return nil
			}
			if rel == "transport" || strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(filepath.Dir(rel))
		for _, decl := range file.Decls {
			name := pkg + "." + declName(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if n.Sel.Name == "MethodPut" {
						puts[name] = true
					}
				case *ast.BasicLit:
					if n.Value == `"PUT"` {
						puts[name] = true
					}
				case *ast.KeyValueExpr:
					if k, ok := n.Key.(*ast.Ident); ok && k.Name == "Idempotent" {
						if v, ok := n.Value.(*ast.Ident); !ok || v.Name != "false" {
							posts[name] = true
						}
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the source: %v", err)
	}

	if got := slices.Sorted(maps.Keys(puts)); !slices.Equal(got, walkedPuts) {
		t.Errorf("the functions that build a PUT are %v, and walkedPuts names %v. "+
			"A new one goes into TestNoReplayedRequestCarriesAnAppendingUpdate's "+
			"walk, so what it sends is checked", got, walkedPuts)
	}
	if got := slices.Sorted(maps.Keys(posts)); !slices.Equal(got, replayablePosts) {
		t.Errorf("the functions that mark a request Idempotent are %v, and "+
			"replayablePosts names %v. A marked request is replayed after a 5xx "+
			"whatever its method, so a new one is a request somebody has to "+
			"show is safe to send twice", got, replayablePosts)
	}
}

// declName is a top-level declaration's name as the lists spell it: a method
// as Type.Method, a function by its name, and anything else by the kind of
// declaration it is, which no list names and so fails if it builds a request.
func declName(decl ast.Decl) string {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok {
		return "<package-level declaration>"
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	if id, ok := recv.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
