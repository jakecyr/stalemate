// Package repo extracts deterministic dependency and Go API evidence without
// executing repository code. Unknown dependency surfaces remain possible exposure.
package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jakecyr/stalemate/internal/domain"
	"github.com/jakecyr/stalemate/internal/privacy"
	"golang.org/x/mod/modfile"
)

func Git(ctx context.Context, path string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=false", "-C", path}, args...)...)
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	b, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("git %s failed", strings.Join(args, " "))
	}
	return string(b), nil
}
func Canonical(path string) (string, error) {
	a, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	a, err = filepath.EvalSymlinks(a)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(a)
	if err != nil || !fi.IsDir() {
		return "", errors.New("project must be an existing directory")
	}
	return a, nil
}

func Build(ctx context.Context, path string, f *privacy.Filter) (domain.Snapshot, error) {
	s := domain.Snapshot{APIs: map[string]string{}, Bodies: map[string]string{}, Dependencies: []domain.Dependency{}, Files: []string{}}
	canonical, err := Canonical(path)
	if err != nil {
		return s, err
	}
	path = canonical
	_, err = Git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return s, errors.New("project must be a Git repository")
	}
	s.Revision, _ = Git(ctx, path, "rev-parse", "HEAD")
	s.Revision = strings.TrimSpace(s.Revision)
	if s.Revision == "" {
		s.Revision = "unborn"
	}
	fileList, err := Git(ctx, path, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return s, err
	}
	paths := strings.Split(fileList, "\x00")
	if len(paths) > 20000 {
		return s, errors.New("repository has over 20000 files; narrow the registered project")
	}
	sort.Strings(paths)
	var fingerprint strings.Builder
	var sourceFingerprint strings.Builder
	rawBodies := map[string]string{}
	localCalls := map[string][]string{}
	var totalBytes int64
	imports := map[string]map[string]bool{}
	importFiles := map[string][]string{}
	replaces := map[string]string{}
	requires := map[string]string{}
	if b, e := readManifest(path, "go.mod"); e == nil {
		m, e := modfile.Parse("go.mod", b, nil)
		if e != nil {
			return s, fmt.Errorf("go.mod: %s", f.Text(e.Error()))
		}
		if m.Module != nil {
			s.Module = m.Module.Mod.Path
		}
		for _, r := range m.Require {
			requires[r.Mod.Path] = r.Mod.Version
		}
		for _, r := range m.Replace {
			if r.New.Version == "" {
				target := r.New.Path
				if !filepath.IsAbs(target) {
					target = filepath.Join(path, target)
				}
				if target, e = Canonical(target); e == nil {
					replaces[r.Old.Path] = target
				}
			}
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return s, e
	}
	var manifest struct {
		Name            string            `json:"name"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if b, e := readManifest(path, "package.json"); e == nil {
		if e = json.Unmarshal(b, &manifest); e != nil {
			return s, fmt.Errorf("package.json: %w", e)
		}
		s.PackageName = manifest.Name
		for name, v := range manifest.Dependencies {
			s.Dependencies = append(s.Dependencies, nodeDep(path, name, v))
		}
		for name, v := range manifest.DevDependencies {
			if _, ok := manifest.Dependencies[name]; !ok {
				s.Dependencies = append(s.Dependencies, nodeDep(path, name, v))
			}
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return s, e
	}
	seen := map[string]bool{}
	for _, rel := range paths {
		if err = ctx.Err(); err != nil {
			return s, err
		}
		if rel == "" || seen[rel] || !f.Allowed(rel) {
			continue
		}
		seen[rel] = true
		full := filepath.Join(path, rel)
		if !privacy.Within(path, full) {
			continue
		}
		info, e := os.Lstat(full)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return s, e
		}
		if !info.Mode().IsRegular() {
			continue
		}
		resolved, e := filepath.EvalSymlinks(full)
		if e != nil {
			return s, e
		}
		if !privacy.Within(path, resolved) {
			continue
		}
		if info.Size() > 2<<20 {
			continue
		}
		b, e := os.ReadFile(full)
		if e != nil {
			return s, e
		}
		if bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		totalBytes += int64(len(b))
		if totalBytes > 128<<20 {
			return s, errors.New("repository text exceeds 128 MiB; narrow the registered project")
		}
		fmt.Fprintf(&fingerprint, "%s:%s\n", rel, domain.ID(string(b)))
		switch filepath.Ext(rel) {
		case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
			if !strings.Contains(rel, ".test.") && !strings.Contains(rel, ".spec.") {
				fmt.Fprintf(&sourceFingerprint, "%s:%s\n", rel, domain.ID(string(b)))
			}
		}
		s.Files = append(s.Files, rel)
		if strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") {
			if s.Module == "" {
				continue
			}
			if err = goFile(rel, b, s.Module, &s, imports, importFiles, rawBodies, localCalls); err != nil {
				return s, err
			}
		}
	}
	for imp, apis := range imports {
		name := imp
		best := 0
		for mod := range requires {
			if moduleMatch(imp, mod) && len(mod) > best {
				name = mod
				best = len(mod)
			}
		}
		for mod := range replaces {
			if moduleMatch(imp, mod) && len(mod) > best {
				name = mod
				best = len(mod)
			}
		}
		if moduleMatch(imp, s.Module) {
			continue
		}
		// Standard-library imports cannot be registered project dependencies.
		if !strings.Contains(strings.Split(name, "/")[0], ".") && replaces[name] == "" {
			continue
		}
		a := []string{}
		for api := range apis {
			a = append(a, imp+"."+api)
		}
		sort.Strings(a)
		s.Dependencies = append(s.Dependencies, domain.Dependency{Name: name, Path: replaces[name], Version: requires[name], APIs: a, Evidence: strings.Join(importFiles[imp], ", ") + "; go.mod", Kind: "go"})
	}
	sort.Slice(s.Dependencies, func(i, j int) bool {
		return s.Dependencies[i].Name+strings.Join(s.Dependencies[i].APIs, ",") < s.Dependencies[j].Name+strings.Join(s.Dependencies[j].APIs, ",")
	})
	for key, value := range s.APIs {
		s.APIs[key] = f.Text(value)
	}
	// Fingerprints include local callees so a changed private helper cannot
	// silently invalidate an exported function's observed implementation.
	for key := range s.Bodies {
		reachable := map[string]string{}
		var visit func(string)
		visit = func(k string) {
			if _, ok := reachable[k]; ok {
				return
			}
			body, ok := rawBodies[k]
			if !ok {
				return
			}
			reachable[k] = body
			for _, callee := range localCalls[k] {
				visit(callee)
			}
		}
		visit(key)
		s.Bodies[key] = domain.ID(domain.JSON(reachable))
	}
	s.SourceHash = domain.ID(sourceFingerprint.String())
	s.Fingerprint = domain.ID(s.Revision, fingerprint.String())
	// Only permitted filenames are passed to Git; secret files never enter diff output.
	if len(s.Files) > 0 && s.Revision != "unborn" {
		args := []string{"diff", "--no-ext-diff", "--no-textconv", "--unified=2", "HEAD", "--"}
		args = append(args, s.Files...)
		if d, e := Git(ctx, path, args...); e == nil {
			s.Diff = f.Text(truncate(d, 24000))
		}
	}
	return s, nil
}
func readManifest(root, name string) ([]byte, error) {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, fmt.Errorf("%s must be a regular file of at most 2 MiB", name)
	}
	return os.ReadFile(path)
}
func moduleMatch(imp, mod string) bool { return imp == mod || strings.HasPrefix(imp, mod+"/") }
func nodeDep(root, name, v string) domain.Dependency {
	d := domain.Dependency{Name: name, Version: v, Evidence: "package.json", Kind: "npm", APIs: []string{}}
	for _, prefix := range []string{"file:", "link:"} {
		if strings.HasPrefix(v, prefix) {
			p := strings.TrimPrefix(v, prefix)
			if !filepath.IsAbs(p) {
				p = filepath.Join(root, p)
			}
			d.Path, _ = Canonical(p)
		}
	}
	return d
}
func format(n any) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, token.NewFileSet(), n)
	return b.String()
}
func funcSignature(t *ast.FuncType) string {
	clone := *t
	clean := func(fl *ast.FieldList) *ast.FieldList {
		if fl == nil {
			return nil
		}
		out := &ast.FieldList{}
		for _, field := range fl.List {
			n := len(field.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				out.List = append(out.List, &ast.Field{Type: field.Type})
			}
		}
		return out
	}
	clone.Params = clean(t.Params)
	clone.Results = clean(t.Results)
	return format(&clone)
}
func goFile(rel string, b []byte, module string, s *domain.Snapshot, imports map[string]map[string]bool, files map[string][]string, rawBodies map[string]string, localCalls map[string][]string) error {
	f, err := parser.ParseFile(token.NewFileSet(), rel, b, 0)
	if err != nil {
		return fmt.Errorf("parse %s: %w", rel, err)
	}
	pkg := module
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir != "." {
		pkg += "/" + dir
	}
	aliases := map[string]string{}
	for _, i := range f.Imports {
		imp, e := strconv.Unquote(i.Path.Value)
		if e != nil {
			continue
		}
		alias := filepath.Base(imp)
		if i.Name != nil {
			alias = i.Name.Name
		}
		aliases[alias] = imp
		if imports[imp] == nil {
			imports[imp] = map[string]bool{}
		}
		files[imp] = append(files[imp], rel)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if x, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := x.X.(*ast.Ident); ok {
				if imp := aliases[id.Name]; imp != "" {
					imports[imp][x.Sel.Name] = true
				}
			}
		}
		return true
	})
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			receiverName, receiverType := "", ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recv := format(d.Recv.List[0].Type)
				recv = strings.TrimPrefix(recv, "*")
				name = recv + "." + name
				receiverType = recv
				if len(d.Recv.List[0].Names) > 0 {
					receiverName = d.Recv.List[0].Names[0].Name
				}
			}
			key := pkg + "." + name
			rawBodies[key] = domain.ID(format(d.Body))
			if d.Body != nil {
				ast.Inspect(d.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch fn := call.Fun.(type) {
					case *ast.Ident:
						localCalls[key] = append(localCalls[key], pkg+"."+fn.Name)
					case *ast.SelectorExpr:
						if id, ok := fn.X.(*ast.Ident); ok && id.Name == receiverName {
							localCalls[key] = append(localCalls[key], pkg+"."+receiverType+"."+fn.Sel.Name)
						}
					}
					return true
				})
			}
			if ast.IsExported(d.Name.Name) {
				s.APIs[key] = funcSignature(d.Type)
				s.Bodies[key] = rawBodies[key]
			}
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					if ast.IsExported(sp.Name.Name) {
						s.APIs[pkg+"."+sp.Name.Name] = format(sp.Type)
					}
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						if ast.IsExported(n.Name) {
							value := format(sp.Type)
							for _, v := range sp.Values {
								value += "=" + format(v)
							}
							s.APIs[pkg+"."+n.Name] = value
						}
					}
				}
			}
		}
	}
	return nil
}

func Changes(before, after domain.Snapshot) []domain.Change {
	out := []domain.Change{}
	for api, old := range before.APIs {
		next, ok := after.APIs[api]
		kind := "signature_changed"
		if !ok {
			kind = "removed"
		}
		if next != old || !ok {
			out = append(out, domain.Change{API: api, Before: old, After: next, Kind: kind})
		} else if before.Bodies[api] != "" && before.Bodies[api] != after.Bodies[api] {
			out = append(out, domain.Change{API: api, Before: old, After: next, Kind: "behavior_changed"})
		}
	}
	if len(out) == 0 && before.PackageName != "" && before.SourceHash != "" && before.SourceHash != after.SourceHash {
		out = append(out, domain.Change{API: before.PackageName, Kind: "implementation_changed", Before: before.SourceHash, After: after.SourceHash})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].API < out[j].API })
	return out
}
func Relevant(edge domain.Relationship, changes []domain.Change) []domain.Change {
	if len(edge.APIs) == 0 {
		return changes
	}
	out := []domain.Change{}
	for _, c := range changes {
		for _, api := range edge.APIs {
			if c.API == api || strings.HasPrefix(c.API, api+".") {
				out = append(out, c)
				break
			}
			// Methods may be reached through factories without a Type selector.
			if i := strings.LastIndex(api, "."); i >= 0 {
				pkg := api[:i+1]
				if strings.HasPrefix(c.API, pkg) && strings.Contains(strings.TrimPrefix(c.API, pkg), ".") {
					out = append(out, c)
					break
				}
			}
		}
	}
	return out
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "\n[truncated]"
}

// DiffSince provides commit evidence when HEAD moved between scans.
func DiffSince(ctx context.Context, path string, old domain.Snapshot, next domain.Snapshot, f *privacy.Filter) string {
	if old.Revision == "" || old.Revision == "unborn" || old.Revision == next.Revision {
		return next.Diff
	}
	files := []string{}
	for _, v := range append(old.Files, next.Files...) {
		if f.Allowed(v) {
			files = append(files, v)
		}
	}
	if len(files) == 0 {
		return next.Diff
	}
	sort.Strings(files)
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--unified=2", old.Revision, "--"}
	args = append(args, files...)
	d, err := Git(ctx, path, args...)
	if err != nil {
		return next.Diff
	}
	return f.Text(truncate(d, 24000))
}
