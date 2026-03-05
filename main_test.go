package main

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestExtractTargets(t *testing.T) {
	pkgs := []*packages.Package{
		{
			Errors: []packages.Error{
				{Pos: "main.go:5:2", Msg: "\"fmt\" imported and not used"},
				{Pos: "main.go:10:5", Msg: "x declared and not used"},
				{Pos: "main.go:15:6", Msg: "helper declared and not used"},
				{Pos: "main.go:20:1", Msg: "invalid syntax"}, // Should be ignored
			},
		},
	}

	targets := extractTargets(pkgs)

	if len(targets) != 3 {
		t.Fatalf("Expected 3 targets, got %d", len(targets))
	}

	expected := []Target{
		{File: "main.go", Line: 5, Identifier: "", Kind: TargetImport},
		{File: "main.go", Line: 10, Identifier: "x", Kind: TargetDecl},
		{File: "main.go", Line: 15, Identifier: "helper", Kind: TargetDecl},
	}

	for i, tgt := range expected {
		if targets[i].File != tgt.File || targets[i].Line != tgt.Line ||
			targets[i].Identifier != tgt.Identifier ||
			targets[i].Kind != tgt.Kind {
			t.Errorf("Target %d mismatch. Expected %+v, got %+v", i, tgt, targets[i])
		}
	}
}

func TestApplyRewrite(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		target     Target
		expected   string
		isModified bool
	}{
		{
			name:       "Unused Import",
			src:        "package main\n\nimport \"fmt\"\n\nfunc main() {}",
			target:     Target{File: "test.go", Line: 3, Kind: TargetImport},
			expected:   "package main\n\nfunc main() {}\n",
			isModified: true,
		},
		{
			name:       "Unused Grouped Import",
			src:        "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n\t_ = os.Args\n}",
			target:     Target{File: "test.go", Line: 4, Kind: TargetImport},
			expected:   "package main\n\nimport (\n\t\"os\"\n)\n\nfunc main() {\n\t_ = os.Args\n}\n",
			isModified: true,
		},
		{
			name:       "Cleanup Empty Import Block",
			src:        "package main\n\nimport (\n\t\"fmt\"\n)\n\nfunc main() {}",
			target:     Target{File: "test.go", Line: 4, Kind: TargetImport},
			expected:   "package main\n\nfunc main() {}\n",
			isModified: true,
		},
		{
			name:       "Unused Variable",
			src:        "package main\n\nfunc main() {\n\tvar x = 1\n}",
			target:     Target{File: "test.go", Line: 4, Identifier: "x", Kind: TargetDecl},
			expected:   "package main\n\nfunc main() {\n\n}\n",
			isModified: true,
		},
		{
			name:       "Unused Constant",
			src:        "package main\n\nfunc main() {\n\tconst x = 1\n}",
			target:     Target{File: "test.go", Line: 4, Identifier: "x", Kind: TargetDecl},
			expected:   "package main\n\nfunc main() {\n\n}\n",
			isModified: true,
		},
		{
			name:       "Unused Grouped Variable",
			src:        "package main\n\nvar (\n\ta = 1\n\tb = 2\n)\n\nfunc main() {\n\t_ = b\n}",
			target:     Target{File: "test.go", Line: 4, Identifier: "a", Kind: TargetDecl},
			expected:   "package main\n\nvar (\n\tb = 2\n)\n\nfunc main() {\n\t_ = b\n}\n",
			isModified: true,
		},
		{
			name:       "Cleanup Empty Var Block",
			src:        "package main\n\nvar (\n\ta = 1\n)\n\nfunc main() {}",
			target:     Target{File: "test.go", Line: 4, Identifier: "a", Kind: TargetDecl},
			expected:   "package main\n\nfunc main() {}\n",
			isModified: true,
		},
		{
			name:       "Unused Variable in Multi Assignment",
			src:        "package main\n\nfunc main() {\n\tvar a, b = 1, 2\n\t_ = b\n}",
			target:     Target{File: "test.go", Line: 4, Identifier: "a", Kind: TargetDecl},
			expected:   "package main\n\nfunc main() {\n\tvar b = 2\n\t_ = b\n}\n",
			isModified: true,
		},
		{
			name:       "Unused Variable in Multi-Return Call",
			src:        "package main\n\nfunc foo() (int, int) { return 1, 2 }\n\nfunc main() {\n\tvar a, b = foo()\n\t_ = b\n}",
			target:     Target{File: "test.go", Line: 6, Identifier: "a", Kind: TargetDecl},
			expected:   "package main\n\nfunc foo() (int, int) { return 1, 2 }\n\nfunc main() {\n\tvar _, b = foo()\n\t_ = b\n}\n",
			isModified: true,
		},
		{
			name:       "Unused Short Assignment (Replace with _)",
			src:        "package main\n\nfunc foo() (int, int) { return 1, 2 }\n\nfunc main() {\n\ta, b := foo()\n\t_ = b\n}",
			target:     Target{File: "test.go", Line: 6, Identifier: "a", Kind: TargetDecl},
			expected:   "package main\n\nfunc foo() (int, int) { return 1, 2 }\n\nfunc main() {\n\t_, b := foo()\n\t_ = b\n}\n",
			isModified: true,
		},
		{
			name:       "Unused Function",
			src:        "package main\n\nfunc helper() {}\n\nfunc main() {}",
			target:     Target{File: "test.go", Line: 3, Identifier: "helper", Kind: TargetDecl},
			expected:   "package main\n\nfunc main() {}\n",
			isModified: true,
		},
		{
			name:       "Unused Type",
			src:        "package main\n\ntype Foo struct{}\n\nfunc main() {}",
			target:     Target{File: "test.go", Line: 3, Identifier: "Foo", Kind: TargetDecl},
			expected:   "package main\n\nfunc main() {}\n",
			isModified: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "test.go", tt.src, parser.ParseComments)
			if err != nil {
				t.Fatalf("Failed to parse src: %v", err)
			}

			modified := applyRewrite(fset, f, tt.target)
			if modified != tt.isModified {
				t.Errorf("Expected modified=%v, got %v", tt.isModified, modified)
			}

			var buf bytes.Buffer
			if err := format.Node(&buf, fset, f); err != nil {
				t.Fatalf("Failed to format node: %v", err)
			}

			// Normalize newlines for strict comparison
			got := strings.ReplaceAll(buf.String(), "\r\n", "\n")
			expected := strings.ReplaceAll(tt.expected, "\r\n", "\n")

			if got != expected {
				t.Errorf("Rewrite mismatch.\nExpected:\n%s\nGot:\n%s", expected, got)
			}
		})
	}
}

func TestApplyCommentText(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		target     Target
		expected   string
		isModified bool
	}{
		{
			name:       "Comment Entire Function",
			src:        "package main\n\nfunc helper() {\n\tprintln(\"test\")\n}\n\nfunc main() {}",
			target:     Target{File: "test.go", Line: 3, Identifier: "helper", Kind: TargetDecl},
			expected:   "package main\n\n// func helper() {\n// \tprintln(\"test\")\n// }\n\nfunc main() {}",
			isModified: true,
		},
		{
			name:   "Fallback on Shared Variable Declaration",
			src:    "package main\n\nfunc main() {\n\tvar a, b = 1, 2\n\t_ = b\n}",
			target: Target{File: "test.go", Line: 4, Identifier: "a", Kind: TargetDecl},
			// Expect `a` to be removed, not commented, leaving `var b = 2`
			expected:   "package main\n\nfunc main() {\n\tvar b = 2\n\t_ = b\n}\n",
			isModified: true,
		},
		{
			name:   "Fallback on Short Assignment",
			src:    "package main\n\nfunc foo() (int, int) { return 1, 2 }\n\nfunc main() {\n\ta, b := foo()\n\t_ = b\n}",
			target: Target{File: "test.go", Line: 6, Identifier: "a", Kind: TargetDecl},
			// Expect `a` to be replaced with `_`, not commented
			expected:   "package main\n\nfunc foo() (int, int) { return 1, 2 }\n\nfunc main() {\n\t_, b := foo()\n\t_ = b\n}\n",
			isModified: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "test.go")

			if err := os.WriteFile(testFile, []byte(tt.src), 0o600); err != nil {
				t.Fatalf("Failed to write temp file: %v", err)
			}

			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, testFile, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("Failed to parse temp file: %v", err)
			}

			// Point the target to our dynamically generated temp file path
			target := tt.target
			target.File = testFile

			// Ensure dryRun is strictly false for the test to write to our temp file
			dryRun = false

			modified := applyCommentText(testFile, fset, f, target)
			if modified != tt.isModified {
				t.Errorf("Expected applyCommentText to return %v, got %v", tt.isModified, modified)
			}

			content, err := os.ReadFile(testFile)
			if err != nil {
				t.Fatalf("Failed to read modified temp file: %v", err)
			}

			got := strings.ReplaceAll(string(content), "\r\n", "\n")
			expected := strings.ReplaceAll(tt.expected, "\r\n", "\n")

			if got != expected {
				t.Errorf("CommentText mismatch.\nExpected:\n%s\nGot:\n%s", expected, got)
			}
		})
	}
}
