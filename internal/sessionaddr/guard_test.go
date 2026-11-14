package sessionaddr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Keep routing semantics centralized. These are source contracts, in addition
// to runtime collision/missing-owner tests: a local slash heuristic or copied
// channel/chat concatenation must not silently reappear in a later patch.
func TestRoutingConstructionGuard(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, dir := range []string{"agent", "tools", "serverapp", "channel/web", "channel/cli"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			name := filepath.Join(root, dir, e.Name())
			fs := token.NewFileSet()
			f, err := parser.ParseFile(fs, name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if fun, ok := call.Fun.(*ast.SelectorExpr); ok && fun.Sel.Name == "Contains" && len(call.Args) == 2 {
						lit, ok := call.Args[1].(*ast.BasicLit)
						if ok && lit.Value == `"/"` {
							ast.Inspect(call.Args[0], func(x ast.Node) bool {
								if sel, ok := x.(*ast.SelectorExpr); ok && sel.Sel.Name == "AgentID" {
									t.Errorf("%s: infer identity through sessionaddr.IsSubAgent, never slash shape", fs.Position(call.Pos()))
								}
								return true
							})
						}
					}
				}
				b, ok := n.(*ast.BinaryExpr)
				if !ok || b.Op != token.ADD {
					return true
				}
				left, ok := b.X.(*ast.BinaryExpr)
				if !ok || left.Op != token.ADD {
					return true
				}
				colon, ok := left.Y.(*ast.BasicLit)
				if !ok || colon.Value != `":"` {
					return true
				}
				l, r := fieldName(left.X), fieldName(b.Y)
				if (l == "channel" || l == "Channel" || l == "originChannel" || l == "chName" || l == "ch" || l == "channelName" || l == "rootChannel" || l == "physicalChannel" || l == "ptyChannel" || l == "parentChannel") && (r == "chatID" || r == "ChatID" || r == "originChatID" || r == "chat" || r == "id" || r == "rootChatID" || r == "parentChatID") {
					t.Errorf("%s: session address serialization must use sessionaddr.Key", fs.Position(b.Pos()))
				}
				return true
			})
		}
	}
}

func fieldName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return ""
}
