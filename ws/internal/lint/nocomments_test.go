package lint

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var directives = []string{"//go:", "//nolint", "//line ", "//export ", "// +build", "//lint:"}

func moduleRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func isDirective(text string) bool {
	for _, p := range directives {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

func TestNoComments(t *testing.T) {
	root := moduleRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "dist" || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, g := range f.Comments {
			for _, c := range g.List {
				if !isDirective(c.Text) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s:%d: comment not allowed: %.60s", rel, fset.Position(c.Pos()).Line, c.Text)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
