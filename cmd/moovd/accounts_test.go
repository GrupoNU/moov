package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The M1×M2 revocation seam, pinned where it is actually made.
//
// Contract §2.4 promises that suspend and delete revoke "every session and
// token" of an account, and §3.4 makes a delegated session outlive any single
// request by twelve hours. Those two sentences meet in exactly one statement:
// serverRevoker.RevokeAccount calling Server.RevokeDelegatedSessions.
//
// # Why this is a source assertion and not a behavioral one
//
// The honest behavioral test — suspend an account over the accounts API,
// then present its delegated session and watch it fail — needs a live server,
// a database, a Mailcow, and a signed token from a configured issuer. That
// test belongs to the end-to-end gate (F5), and it is the one that will prove
// the behavior.
//
// What can be lost silently LONG before that gate runs is the call itself.
// Each half is independently well tested: jmaphttp proves
// RevokeDelegatedSessions kills sessions, and internal/accounts proves suspend
// calls its SessionRevoker. Both suites stay green if the one line joining
// them is deleted, because each half's fake stands in for the other. The
// failure that would follow is invisible in exactly the way that matters: a
// suspended mailbox keeps serving mail to an open portal tab for up to twelve
// hours, and every test still passes.
//
// So this pins the join, cheaply and without a fixture, by reading the source.
// A refactor that renames or restructures the call will fail here and ask for
// a deliberate update, which is the point.
func TestSuspendRevokesDelegatedSessions(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "accounts.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing accounts.go: %v", err)
	}

	const (
		recv   = "serverRevoker"
		method = "RevokeAccount"
		want   = "RevokeDelegatedSessions"
	)

	fn := findMethod(file, recv, method)
	if fn == nil {
		t.Fatalf("%s.%s not found in accounts.go; if it moved, move this test's "+
			"assertion with it — do not delete it", recv, method)
	}

	if !callsSelector(fn, want) {
		t.Errorf("%s.%s does not call %s.\n\n"+
			"This is the M1×M2 seam: without it, suspending or deleting an account "+
			"leaves its delegated sessions alive for up to the session TTL, and a "+
			"suspended mailbox keeps serving mail to an already-open portal tab. "+
			"Contract §2.4 requires every session revoked.",
			recv, method, want)
	}
}

// findMethod returns the named method of the named receiver type, or nil.
func findMethod(file *ast.File, recvType, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Name.Name != name {
			continue
		}
		if receiverName(fn.Recv.List[0].Type) == recvType {
			return fn
		}
	}
	return nil
}

// receiverName is the type name of a receiver, with any pointer stripped.
func receiverName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// callsSelector reports whether fn contains a call of the form x.sel(...).
func callsSelector(fn *ast.FuncDecl, sel string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if s, ok := call.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == sel {
			found = true
			return false
		}
		return true
	})
	return found
}
