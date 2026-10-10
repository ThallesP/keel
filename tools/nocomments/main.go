package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	fset := token.NewFileSet()
	found := 0
	for _, dir := range os.Args[1:] {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			fail(err)
		}
		for _, path := range files {
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				fail(err)
			}
			if ast.IsGenerated(f) {
				continue
			}
			for _, group := range f.Comments {
				for _, c := range group.List {
					if strings.HasPrefix(c.Text, "//go:") {
						continue
					}
					found++
					line, _, _ := strings.Cut(c.Text, "\n")
					fmt.Printf("%s: %s\n", fset.Position(c.Pos()), line)
				}
			}
		}
	}
	if found > 0 {
		fmt.Printf("\n%d comments. Go code carries none (CLAUDE.md, Code rules): say it with a name, a type or a test, and put the reason in docs/ or the commit message.\n", found)
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
