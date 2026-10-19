package repo

import (
	"go/ast"
	"go/parser"
	"testing"
)

func mustFunc(t *testing.T, s string) *ast.FuncType {
	t.Helper()
	n, err := parser.ParseExpr(s)
	if err != nil {
		t.Fatal(err)
	}
	return n.(*ast.FuncType)
}
