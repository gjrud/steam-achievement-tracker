package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/keyring"
)

// assertPanicLogScrubbed checks a panic log: the secrets are absent in any letter case, the redaction marker and the
// stack are present, and the panic's own text is kept.
func assertPanicLogScrubbed(t *testing.T, logged string, secrets ...string) {
	t.Helper()
	lower := strings.ToLower(logged)
	for _, s := range secrets {
		if strings.Contains(lower, strings.ToLower(s)) {
			t.Fatalf("panic log %q carries secret %q", logged, s)
		}
	}
	if !strings.Contains(logged, "REDACTED") {
		t.Fatalf("panic log %q has no redaction marker", logged)
	}
	if !strings.Contains(logged, "goroutine") {
		t.Fatalf("panic log %q has no stack", logged)
	}
}

// A panic value that carries the API key (in any case, and escaped) or a SteamID64 is scrubbed from the panic log,
// and the frontend still gets only the generic sentence.
func TestCallBoundErrScrubsSecretsFromPanicLog(t *testing.T) {
	const key = "0123456789ABCDEF0123456789ABCDEF"
	const steamID = "76561198000000000"
	logs := captureLog(t)
	err := callBoundErr("SaveAPIKey", func() error {
		panic(fmt.Errorf("backend refused %s (%s) id %s", key, url.QueryEscape(strings.ToLower(key)), steamID))
	})
	if err == nil || err.Error() != msgGeneric {
		t.Fatalf("err = %v, want %q", err, msgGeneric)
	}
	logged := logs.String()
	if !strings.Contains(logged, "SaveAPIKey: panic: backend refused") {
		t.Fatalf("log = %q, want the op and the panic text", logged)
	}
	assertPanicLogScrubbed(t, logged, key, steamID)
}

// The real SaveAPIKey: the keyring store panics with the key in the panic value. The panic log must not carry the key,
// and the caller gets the generic sentence.
func TestSaveAPIKeyPanicLogScrubsKey(t *testing.T) {
	const key = "0123456789ABCDEF0123456789ABCDEF"
	app := newTestApp(t)
	prevStore := storeSteamAPIKey
	storeSteamAPIKey = func(k string) (string, error) { panic("keyring: " + k) }
	t.Cleanup(func() { storeSteamAPIKey = prevStore })

	logs := captureLog(t)
	err := app.SaveAPIKey(key)
	if err == nil || err.Error() != msgGeneric {
		t.Fatalf("SaveAPIKey err = %v, want %q", err, msgGeneric)
	}
	assertPanicLogScrubbed(t, logs.String(), key)
}

// A panic in a bound method's body is logged with its stack, and the frontend gets the generic sentence and a zero
// result. Wails would otherwise pass the panic text to the UI as a runtime error.
func TestCallBoundRecoversPanic(t *testing.T) {
	logs := captureLog(t)
	got, err := callBound("Op", func() (int, error) {
		panic(&fs.PathError{Op: "open", Path: "/srv/secret-dir/library.jpg", Err: fs.ErrPermission})
	})
	if got != 0 {
		t.Fatalf("result = %d, want the zero value", got)
	}
	if err == nil || err.Error() != msgGeneric {
		t.Fatalf("err = %v, want %q", err, msgGeneric)
	}
	logged := logs.String()
	if !strings.Contains(logged, "Op: panic: open library.jpg: permission denied") {
		t.Fatalf("log = %q, want the op, the panic and the redacted panic value", logged)
	}
	if strings.Contains(logged, "/srv/secret-dir") {
		t.Fatalf("log = %q carries the panic's path", logged)
	}
	if !strings.Contains(logged, "goroutine") || !strings.Contains(logged, "TestCallBoundRecoversPanic") {
		t.Fatalf("log = %q, want the stack of the panic", logged)
	}
}

func TestCallBoundErrRecoversPanic(t *testing.T) {
	logs := captureLog(t)
	err := callBoundErr("Op", func() error { panic("index out of range [3] with length 2") })
	if err == nil || err.Error() != msgGeneric {
		t.Fatalf("err = %v, want %q", err, msgGeneric)
	}
	if !strings.Contains(logs.String(), "Op: panic: index out of range") || !strings.Contains(logs.String(), "goroutine") {
		t.Fatalf("log = %q, want the panic with its stack", logs.String())
	}
}

// Without a panic, callBound returns the result and passes the error through userFacingError: a sentinel comes back
// unchanged, and a raw error becomes the generic sentence.
func TestCallBoundPassesResultAndErrorThrough(t *testing.T) {
	captureLog(t)
	got, err := callBound("Op", func() (int, error) { return 7, nil })
	if got != 7 || err != nil {
		t.Fatalf("callBound(ok) = %d, %v; want 7, nil", got, err)
	}
	got, err = callBound("Op", func() (int, error) { return 7, keyring.ErrMissing })
	if got != 7 || err != keyring.ErrMissing {
		t.Fatalf("callBound(sentinel) = %d, %v; want 7 and the sentinel itself", got, err)
	}
	if err := callBoundErr("Op", func() error { return errors.New("raw /x/y") }); err == nil || err.Error() != msgGeneric {
		t.Fatalf("callBoundErr(raw) = %v, want %q", err, msgGeneric)
	}
}

// Every exported App method that returns an error must be a single return of callBound or callBoundErr, with its own
// name, so no bound method can skip the panic guard or userFacingError. The lifecycle hooks are not bound and are
// exempt. The check reads the source, so a new method fails here until it is written that way.
func TestBoundMethodsUseCallBound(t *testing.T) {
	lifecycle := map[string]bool{}
	for _, name := range lifecycleErrorMethods {
		lifecycle[name] = true
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 || !fd.Name.IsExported() || lifecycle[fd.Name.Name] {
				continue
			}
			star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
			if !ok || !isIdent(star.X, "App") {
				continue
			}
			results := fd.Type.Results
			if results == nil || len(results.List) == 0 || !isIdent(results.List[len(results.List)-1].Type, "error") {
				continue
			}
			name := fd.Name.Name
			if call := onlyCallBoundReturn(fd); call == nil {
				t.Errorf("exported App method %s returns an error but its body is not a single return of callBound or callBoundErr", name)
			} else if got := boundOpName(call); got != name {
				t.Errorf("exported App method %s calls %s with op %q, want %q", name, call.Fun.(*ast.Ident).Name, got, name)
			} else {
				found[name] = true
			}
		}
	}
	for _, name := range boundErrorMethods {
		if !found[name] {
			t.Errorf("bound method %s does not go through callBound or callBoundErr", name)
		}
	}
	if len(found) != len(boundErrorMethods) {
		t.Fatalf("found %d error-returning App methods through callBound, boundErrorMethods lists %d", len(found), len(boundErrorMethods))
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// onlyCallBoundReturn returns the call of fd's body when the body is one return of callBound or callBoundErr, else nil.
func onlyCallBoundReturn(fd *ast.FuncDecl) *ast.CallExpr {
	if fd.Body == nil || len(fd.Body.List) != 1 {
		return nil
	}
	ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return nil
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return nil
	}
	if !isIdent(call.Fun, "callBound") && !isIdent(call.Fun, "callBoundErr") {
		return nil
	}
	return call
}

// boundOpName returns the string literal that names the operation (the first argument), or "" when it is not one.
func boundOpName(call *ast.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}

// The sync goroutine defers logSyncPanic and then done (startSync), so logSyncPanic runs first and recovers. Here the
// same order is run by hand: the panic is logged, scrubbed, and does not escape, and done still runs afterwards.
func TestLogSyncPanicLogsScrubbedAndDoneStillRuns(t *testing.T) {
	const key = "0123456789ABCDEF0123456789ABCDEF"
	const steamID = "76561198000000000"
	logs := captureLog(t)
	var order []string
	func() {
		defer func() { order = append(order, "done") }()
		defer logSyncPanic()
		order = append(order, "run")
		panic(fmt.Errorf("sync broke with %s for %s", key, steamID))
	}()
	if got, want := strings.Join(order, ","), "run,done"; got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
	if !strings.Contains(logs.String(), "full sync: panic:") {
		t.Fatalf("log = %q, want the sync panic line", logs.String())
	}
	assertPanicLogScrubbed(t, logs.String(), key, steamID)
}
