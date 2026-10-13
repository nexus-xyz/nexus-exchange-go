package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// maxFollow bounds how many aliases deep one line is followed, so an alias cycle cannot hang the
// listing. The compiler rejects such a cycle anyway; this is only a guard.
const maxFollow = 8

// pkg is one package as `go list -json` describes it, plus its parsed files.
type pkg struct {
	ImportPath string
	Name       string
	Dir        string
	GoFiles    []string
	CgoFiles   []string

	files []*ast.File
}

type module struct {
	path string
	fset *token.FileSet
	pkgs map[string]*pkg
}

// Surface lists the public API of the module rooted at dir, sorted.
func Surface(dir string) ([]string, error) {
	m, err := load(dir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for path, p := range m.pkgs {
		if p.Name != "main" && !isInternal(path) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no public package under %s", dir)
	}
	var out []string
	for _, path := range paths {
		p := m.pkgs[path]
		if err := m.parse(p); err != nil {
			return nil, err
		}
		out = append(out, m.listPackage(p)...)
	}
	sort.Strings(out)
	return out, nil
}

// load lists the module's packages. -find: no dependency is resolved, so nothing is downloaded.
// GOWORK=off: a go.work above the tree must not add or replace modules.
func load(dir string) (*module, error) {
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	modPath, err := run("list", "-m", "-f", "{{.Path}}")
	if err != nil {
		return nil, err
	}
	listing, err := run("list", "-find", "-json", "./...")
	if err != nil {
		return nil, err
	}
	m := &module{path: strings.TrimSpace(string(modPath)), fset: token.NewFileSet(), pkgs: map[string]*pkg{}}
	dec := json.NewDecoder(bytes.NewReader(listing))
	for {
		p := new(pkg)
		if err := dec.Decode(p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list output: %v", err)
		}
		m.pkgs[p.ImportPath] = p
	}
	return m, nil
}

func isInternal(path string) bool {
	for _, elem := range strings.Split(path, "/") {
		if elem == "internal" {
			return true
		}
	}
	return false
}

// parse reads p's non-test files, without comments: doc comments are not part of the surface.
func (m *module) parse(p *pkg) error {
	if p.files != nil {
		return nil
	}
	names := append(append([]string{}, p.GoFiles...), p.CgoFiles...)
	sort.Strings(names)
	for _, name := range names {
		f, err := parser.ParseFile(m.fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		p.files = append(p.files, f)
	}
	return nil
}

// scope says how names in one file print. qual is "" in a listed package, whose own names print
// bare, and the package's name in one reached through an alias (models.MarketSummaryStatus).
type scope struct {
	m       *module
	p       *pkg
	file    *ast.File
	qual    string
	tparams map[string]bool
}

func (s scope) withTypeParams(list *ast.FieldList) scope {
	if list == nil {
		return s
	}
	tp := map[string]bool{}
	for name := range s.tparams {
		tp[name] = true
	}
	for _, field := range list.List {
		for _, name := range field.Names {
			tp[name.Name] = true
		}
	}
	s.tparams = tp
	return s
}

// lineQual is how lines name an item of p: the package name for the module's root package, and the
// path below the module for any other.
func (m *module) lineQual(p *pkg) string {
	if p.ImportPath == m.path {
		return p.Name
	}
	return strings.TrimPrefix(p.ImportPath, m.path+"/")
}

func (m *module) listPackage(p *pkg) []string {
	q := m.lineQual(p)
	var out []string
	for _, f := range p.files {
		for _, decl := range f.Decls {
			s := scope{m: m, p: p, file: f}
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv == nil {
					s = s.withTypeParams(d.Type.TypeParams)
					out = append(out, "func "+q+"."+d.Name.Name+s.typeParams(d.Type.TypeParams)+s.signature(d.Type))
				} else if base := recvBase(d.Recv); base != nil && base.IsExported() {
					out = append(out, s.method(q+"."+base.Name, d))
				}
			case *ast.GenDecl:
				out = append(out, s.genDecl(q, d, 0)...)
			}
		}
	}
	return out
}

func (s scope) genDecl(q string, d *ast.GenDecl, depth int) []string {
	var out []string
	switch d.Tok {
	case token.TYPE:
		for _, spec := range d.Specs {
			ts := spec.(*ast.TypeSpec)
			if ts.Name.IsExported() {
				out = append(out, s.typeLines(q+"."+ts.Name.Name, ts, depth)...)
			}
		}
	case token.CONST:
		for _, c := range constEntries(d) {
			if c.name.IsExported() {
				out = append(out, "const "+q+"."+c.name.Name+s.constSuffix(c, depth))
			}
		}
	case token.VAR:
		for _, spec := range d.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if name.IsExported() {
					out = append(out, "var "+q+"."+name.Name+s.varSuffix(vs, i, depth))
				}
			}
		}
	}
	return out
}

// method is a method's line, its receiver printed as declared, (T) or (*T), with recvName for the
// type's name and the receiver's type arguments after it: (*nexus.List[T]).
func (s scope) method(recvName string, d *ast.FuncDecl) string {
	recv := d.Recv.List[0].Type
	star := ""
	if st, ok := recv.(*ast.StarExpr); ok {
		star, recv = "*", st.X
	}
	var params []*ast.Ident
	switch r := recv.(type) {
	case *ast.IndexExpr:
		params = identOf(r.Index)
	case *ast.IndexListExpr:
		for _, index := range r.Indices {
			params = append(params, identOf(index)...)
		}
	}
	args := ""
	if len(params) > 0 {
		names := make([]string, len(params))
		for i, p := range params {
			names[i] = p.Name
		}
		args = "[" + strings.Join(names, ", ") + "]"
		// The receiver's type parameters are in scope for the signature.
		s = s.withTypeParams(&ast.FieldList{List: []*ast.Field{{Names: params}}})
	}
	return "func (" + star + recvName + args + ") " + d.Name.Name + s.signature(d.Type)
}

func identOf(e ast.Expr) []*ast.Ident {
	if id, ok := e.(*ast.Ident); ok {
		return []*ast.Ident{id}
	}
	return nil
}

func recvBase(recv *ast.FieldList) *ast.Ident {
	if recv == nil || len(recv.List) == 0 {
		return nil
	}
	e := recv.List[0].Type
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t
		default:
			return nil
		}
	}
}

// typeLines lists one type under owner: its own line, then one per field or interface method.
// An alias into this module also lists its target's lines, and the target's methods, under owner.
func (s scope) typeLines(owner string, ts *ast.TypeSpec, depth int) []string {
	s = s.withTypeParams(ts.TypeParams)
	tp := s.typeParams(ts.TypeParams)
	if ts.Assign.IsValid() {
		out := []string{"type " + owner + tp + " = " + s.typeString(ts.Type)}
		if target, spec, ok := s.resolveType(ts.Type); ok && depth < maxFollow && spec.TypeParams == nil {
			out = append(out, target.typeLines(owner, spec, depth+1)...)
			out = append(out, target.methodsOf(owner, spec.Name.Name)...)
		}
		return out
	}
	switch t := ts.Type.(type) {
	case *ast.StructType:
		out := []string{"type " + owner + tp + " struct"}
		for _, field := range t.Fields.List {
			if len(field.Names) == 0 {
				if base := embeddedName(field.Type); base != nil && base.IsExported() {
					out = append(out, "type "+owner+" struct { "+s.typeString(field.Type)+" }")
				}
				continue
			}
			for _, name := range field.Names {
				if name.IsExported() {
					out = append(out, "type "+owner+" struct { "+name.Name+" "+s.typeString(field.Type)+" }")
				}
			}
		}
		return out
	case *ast.InterfaceType:
		out := []string{"type " + owner + tp + " interface"}
		for _, field := range t.Methods.List {
			if len(field.Names) == 0 {
				out = append(out, "type "+owner+" interface { "+s.typeString(field.Type)+" }")
				continue
			}
			for _, name := range field.Names {
				if ft, ok := field.Type.(*ast.FuncType); ok && name.IsExported() {
					out = append(out, "type "+owner+" interface { "+name.Name+s.signature(ft)+" }")
				}
			}
		}
		return out
	default:
		return []string{"type " + owner + tp + " " + s.typeString(ts.Type)}
	}
}

// methodsOf lists the exported methods declared on the type called name in s's package, under
// owner (an alias of it in the listed package).
func (s scope) methodsOf(owner, name string) []string {
	var out []string
	for _, f := range s.p.files {
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if !ok || d.Recv == nil || !d.Name.IsExported() {
				continue
			}
			if base := recvBase(d.Recv); base != nil && base.Name == name {
				fs := s
				fs.file = f
				out = append(out, fs.method(owner, d))
			}
		}
	}
	return out
}

func embeddedName(e ast.Expr) *ast.Ident {
	switch t := e.(type) {
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel
	case *ast.IndexExpr:
		return embeddedName(t.X)
	case *ast.IndexListExpr:
		return embeddedName(t.X)
	case *ast.Ident:
		return t
	}
	return nil
}

// resolveType finds the declaration of a type named as pkg.Name, when pkg is one of this
// module's packages. Anything else (a builtin, another module, a generic instantiation) is not
// followed.
func (s scope) resolveType(e ast.Expr) (scope, *ast.TypeSpec, bool) {
	target, name, ok := s.resolveSelector(e)
	if !ok {
		return scope{}, nil, false
	}
	for _, f := range target.p.files {
		for _, decl := range f.Decls {
			if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.TYPE {
				for _, spec := range d.Specs {
					if ts := spec.(*ast.TypeSpec); ts.Name.Name == name {
						target.file = f
						return target, ts, true
					}
				}
			}
		}
	}
	return scope{}, nil, false
}

// resolveSelector maps pkg.Name, where pkg is an import of s.file naming one of this module's
// packages, to a scope in that package and Name.
func (s scope) resolveSelector(e ast.Expr) (scope, string, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return scope{}, "", false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok || s.file == nil {
		return scope{}, "", false
	}
	for _, imp := range s.file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		target, ok := s.m.pkgs[path]
		if !ok {
			continue
		}
		local := target.Name
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if local != x.Name {
			continue
		}
		if err := s.m.parse(target); err != nil {
			return scope{}, "", false
		}
		return scope{m: s.m, p: target, qual: target.Name}, sel.Sel.Name, true
	}
	return scope{}, "", false
}

// constEntry is one name of a const block, with the type and value it takes: its own, or for an
// implicit repetition the last ones given, re-evaluated at its own iota.
type constEntry struct {
	name     *ast.Ident
	typ      ast.Expr
	val      ast.Expr
	iota     int
	implicit bool
}

func constEntries(d *ast.GenDecl) []constEntry {
	var out []constEntry
	var typ ast.Expr
	var vals []ast.Expr
	for i, spec := range d.Specs {
		vs := spec.(*ast.ValueSpec)
		implicit := vs.Type == nil && len(vs.Values) == 0
		if !implicit {
			typ, vals = vs.Type, vs.Values
		}
		for j, name := range vs.Names {
			c := constEntry{name: name, typ: typ, iota: i, implicit: implicit}
			if j < len(vals) {
				c.val = vals[j]
			}
			out = append(out, c)
		}
	}
	return out
}

// constSuffix is what follows a const's name: its type if it has one, and its value. A value that
// names a const of this module is replaced by that const's own type and value.
func (s scope) constSuffix(c constEntry, depth int) string {
	typ := ""
	if c.typ != nil {
		typ = " " + s.typeString(c.typ)
	}
	if target, name, ok := s.resolveSelector(c.val); ok && depth < maxFollow {
		if tc, cs, ok := target.findConst(name); ok {
			resolved := cs.constSuffix(tc, depth+1)
			if typ == "" {
				return resolved
			}
			_, value, _ := strings.Cut(resolved, " = ")
			return typ + " = " + value
		}
	}
	value := s.exprString(c.val)
	if usesIota(c.val) {
		value += " (iota = " + strconv.Itoa(c.iota) + ")"
	}
	return typ + " = " + value
}

// findConst finds the const called name in s's package, and the scope of the file declaring it.
func (s scope) findConst(name string) (constEntry, scope, bool) {
	for _, f := range s.p.files {
		for _, decl := range f.Decls {
			if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.CONST {
				for _, c := range constEntries(d) {
					if c.name.Name == name {
						s.file = f
						return c, s, true
					}
				}
			}
		}
	}
	return constEntry{}, scope{}, false
}

// varSuffix is what follows a var's name: its type, as declared or as the value shows it. A var's
// value is not its API, so it is left out, except a composite literal's, which is what the var is.
func (s scope) varSuffix(vs *ast.ValueSpec, i, depth int) string {
	if vs.Type != nil {
		return " " + s.typeString(vs.Type)
	}
	if len(vs.Values) != len(vs.Names) {
		return " = " + s.exprString(vs.Values[0])
	}
	v := vs.Values[i]
	if target, name, ok := s.resolveSelector(v); ok && depth < maxFollow {
		if spec, j, f, ok := target.findVar(name); ok {
			target.file = f
			return target.varSuffix(spec, j, depth+1)
		}
		if c, cs, ok := target.findConst(name); ok {
			return cs.constSuffix(c, depth+1)
		}
	}
	switch t := v.(type) {
	case *ast.CallExpr:
		if sel, ok := t.Fun.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && (x.Name == "errors" && sel.Sel.Name == "New" || x.Name == "fmt" && sel.Sel.Name == "Errorf") {
				return " error"
			}
		}
	case *ast.CompositeLit:
		if t.Type != nil {
			return " " + s.typeString(t.Type) + " = " + s.exprString(t)
		}
	case *ast.UnaryExpr:
		if lit, ok := t.X.(*ast.CompositeLit); ok && t.Op == token.AND && lit.Type != nil {
			return " *" + s.typeString(lit.Type) + " = " + s.exprString(t)
		}
	}
	return " = " + s.exprString(v)
}

func (s scope) findVar(name string) (*ast.ValueSpec, int, *ast.File, bool) {
	for _, f := range s.p.files {
		for _, decl := range f.Decls {
			if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.VAR {
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for j, n := range vs.Names {
						if n.Name == name {
							return vs, j, f, true
						}
					}
				}
			}
		}
	}
	return nil, 0, nil, false
}

func usesIota(e ast.Expr) bool {
	found := false
	if e != nil {
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "iota" {
				found = true
			}
			return !found
		})
	}
	return found
}

var predeclared = map[string]bool{
	"any": true, "bool": true, "byte": true, "comparable": true, "complex64": true,
	"complex128": true, "error": true, "float32": true, "float64": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true, "rune": true, "string": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
}

func (s scope) ident(name string) string {
	if s.qual == "" || predeclared[name] || s.tparams[name] {
		return name
	}
	return s.qual + "." + name
}

// typeString prints a type expression on one line, func parameter names left out.
func (s scope) typeString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return s.ident(t.Name)
	case *ast.SelectorExpr:
		return s.exprString(t)
	case *ast.StarExpr:
		return "*" + s.typeString(t.X)
	case *ast.ParenExpr:
		return "(" + s.typeString(t.X) + ")"
	case *ast.Ellipsis:
		return "..." + s.typeString(t.Elt)
	case *ast.ArrayType:
		switch {
		case t.Len == nil:
			return "[]" + s.typeString(t.Elt)
		default:
			return "[" + s.exprString(t.Len) + "]" + s.typeString(t.Elt)
		}
	case *ast.MapType:
		return "map[" + s.typeString(t.Key) + "]" + s.typeString(t.Value)
	case *ast.ChanType:
		switch t.Dir {
		case ast.SEND:
			return "chan<- " + s.typeString(t.Value)
		case ast.RECV:
			return "<-chan " + s.typeString(t.Value)
		default:
			return "chan " + s.typeString(t.Value)
		}
	case *ast.FuncType:
		return "func" + s.typeParams(t.TypeParams) + s.signature(t)
	case *ast.IndexExpr:
		return s.typeString(t.X) + "[" + s.typeString(t.Index) + "]"
	case *ast.IndexListExpr:
		return s.typeString(t.X) + "[" + s.typeList(t.Indices) + "]"
	case *ast.UnaryExpr: // ~T in a constraint
		return t.Op.String() + s.typeString(t.X)
	case *ast.BinaryExpr: // A | B in a constraint
		return s.typeString(t.X) + " " + t.Op.String() + " " + s.typeString(t.Y)
	case *ast.StructType:
		var fields []string
		for _, field := range t.Fields.List {
			if len(field.Names) == 0 {
				fields = append(fields, s.typeString(field.Type))
				continue
			}
			for _, name := range field.Names {
				fields = append(fields, name.Name+" "+s.typeString(field.Type))
			}
		}
		if len(fields) == 0 {
			return "struct{}"
		}
		return "struct{ " + strings.Join(fields, "; ") + " }"
	case *ast.InterfaceType:
		var methods []string
		for _, field := range t.Methods.List {
			if ft, ok := field.Type.(*ast.FuncType); ok && len(field.Names) > 0 {
				for _, name := range field.Names {
					methods = append(methods, name.Name+s.signature(ft))
				}
				continue
			}
			methods = append(methods, s.typeString(field.Type))
		}
		if len(methods) == 0 {
			return "interface{}"
		}
		return "interface{ " + strings.Join(methods, "; ") + " }"
	}
	return s.exprString(e)
}

func (s scope) typeList(list []ast.Expr) string {
	parts := make([]string, len(list))
	for i, e := range list {
		parts[i] = s.typeString(e)
	}
	return strings.Join(parts, ", ")
}

// fieldTypes is a parameter or result list as types only, one per name: (a, b int) is (int, int).
func (s scope) fieldTypes(list *ast.FieldList) []string {
	if list == nil {
		return nil
	}
	var out []string
	for _, field := range list.List {
		n := max(len(field.Names), 1)
		for range n {
			out = append(out, s.typeString(field.Type))
		}
	}
	return out
}

// signature is a func type's parameters and results, without the func keyword or names.
func (s scope) signature(ft *ast.FuncType) string {
	out := "(" + strings.Join(s.fieldTypes(ft.Params), ", ") + ")"
	switch results := s.fieldTypes(ft.Results); len(results) {
	case 0:
	case 1:
		out += " " + results[0]
	default:
		out += " (" + strings.Join(results, ", ") + ")"
	}
	return out
}

// typeParams prints [T any, K comparable], names kept: a constraint can refer to them.
func (s scope) typeParams(list *ast.FieldList) string {
	if list == nil || len(list.List) == 0 {
		return ""
	}
	var parts []string
	for _, field := range list.List {
		names := make([]string, len(field.Names))
		for i, name := range field.Names {
			names[i] = name.Name
		}
		parts = append(parts, strings.Join(names, ", ")+" "+s.typeString(field.Type))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

var lineBreak = regexp.MustCompile(`\s*\n\s*`)

// exprString prints any expression with go/printer, on one line.
func (s scope) exprString(e ast.Expr) string {
	if e == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, s.m.fset, e); err != nil {
		return fmt.Sprintf("<%v>", err)
	}
	return lineBreak.ReplaceAllString(buf.String(), " ")
}
