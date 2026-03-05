package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// TargetKind identifies what type of declaration is unused.
type TargetKind int

const (
	TargetDecl TargetKind = iota
	TargetImport
)

// Target represents a single compiler error mapped to a location.
type Target struct {
	File       string
	Line       int
	Identifier string
	Kind       TargetKind
	Msg        string
}

// Global Flags
var (
	maxPasses    int
	dryRun       bool
	interactive  bool
	includeTests bool
	removeMode   bool
	showVersion  bool
	version      = "dev"
	commit       = "none"
	date         = "unknown"
)

func main() {
	flag.IntVar(&maxPasses, "max-passes", 10, "Maximum number of build/fix iterations")
	flag.BoolVar(&dryRun, "dry-run", false, "Print proposed changes but do not modify files")
	flag.BoolVar(&interactive, "interactive", false, "Prompt before removing unused declarations")
	flag.BoolVar(&includeTests, "include-tests", true, "Include test files")
	flag.BoolVar(
		&removeMode,
		"remove",
		false,
		"Remove unused code instead of commenting (default is commenting)",
	)
	flag.BoolVar(&showVersion, "version", false, "Print version information")
	flag.Parse()

	if showVersion {
		fmt.Printf("go-clean-unused %s (commit: %s, date: %s)\n", version, commit, date)
		os.Exit(0)
	}

	dir := "./..."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Tests: includeTests,
	}

	for pass := 1; pass <= maxPasses; pass++ {
		fmt.Printf("[PASS %d] Loading packages...\n", pass)
		pkgs, err := packages.Load(cfg, dir)
		if err != nil {
			log.Fatalf("Failed to load packages: %v", err)
		}

		targets := extractTargets(pkgs)
		if len(targets) == 0 {
			fmt.Printf("✅ Success! No unused declarations found after %d passes.\n", pass-1)
			return
		}

		fmt.Printf("Found %d unused declarations.\n", len(targets))

		changedFiles := processTargets(targets)
		if changedFiles == 0 {
			fmt.Println("⚠️ No safe fixes could be applied. Exiting to prevent infinite loop.")
			break
		}
	}
	fmt.Println("Reached max passes.")
}

// extractTargets parses the raw compilation errors into structured Targets.
func extractTargets(pkgs []*packages.Package) []Target {
	var targets []Target
	seen := make(map[string]bool)

	for _, pkg := range pkgs {
		for _, err := range pkg.Errors {
			// Pos is typically "file:line:col" or "file:line"
			parts := strings.Split(err.Pos, ":")
			if len(parts) < 2 {
				continue
			}
			file := parts[0]
			line, _ := strconv.Atoi(parts[1])
			msg := err.Msg

			var kind TargetKind
			var ident string

			if strings.Contains(msg, "imported and not used") {
				kind = TargetImport
			} else if strings.Contains(msg, "declared and not used") {
				kind = TargetDecl
				ident = strings.Split(msg, " ")[0]
				ident = strings.Trim(ident, "\"")
			} else {
				continue // Skip unsupported compiler errors
			}

			// Deduplicate identical errors
			key := fmt.Sprintf("%s:%d:%s", file, line, ident)
			if seen[key] {
				continue
			}
			seen[key] = true

			targets = append(targets, Target{
				File:       file,
				Line:       line,
				Identifier: ident,
				Kind:       kind,
				Msg:        msg,
			})
		}
	}
	return targets
}

// processTargets applies the AST rewrites for all collected targets.
func processTargets(targets []Target) int {
	files := make(map[string][]Target)

	for _, t := range targets {
		// Safety: Do not automatically remove exported identifiers.
		if t.Kind == TargetDecl && ast.IsExported(t.Identifier) {
			fmt.Printf(
				"  Skipping exported identifier: %s at %s:%d\n",
				t.Identifier,
				t.File,
				t.Line,
			)
			continue
		}
		files[t.File] = append(files[t.File], t)
	}

	changedFilesCount := 0

	for file, fileTargets := range files {
		// Safety Guards
		if isGenerated(file) {
			fmt.Printf("  Skipping generated file: %s\n", file)
			continue
		}
		if isCgo(file) {
			fmt.Printf("  Skipping CGO file: %s\n", file)
			continue
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			fmt.Printf("  Failed to parse %s: %v\n", file, err)
			continue
		}

		// Sort bottom-up by line number to avoid line-shifting issues
		// during sequential interactive prompts (though AST edits are resilient).
		sort.Slice(fileTargets, func(i, j int) bool {
			return fileTargets[i].Line > fileTargets[j].Line
		})

		modified := false
		for _, t := range fileTargets {
			if interactive && !promptUser(t) {
				continue
			}

			if !removeMode { // Commenting is now the default
				if applyCommentText(file, fset, f, t) {
					if dryRun {
						fmt.Printf(
							"  [Dry Run] Would comment %s at %s:%d\n",
							t.Identifier,
							t.File,
							t.Line,
						)
					} else {
						fmt.Printf("  Commented unused %s at %s:%d\n", t.Identifier, t.File, t.Line)
					}
					// Re-parse the AST for subsequent targets in the same file since text changed
					fset = token.NewFileSet()
					f, _ = parser.ParseFile(fset, file, nil, parser.ParseComments)
					modified = false // Already written to disk in applyCommentText
				}
			} else {
				if applyRewrite(fset, f, t) {
					if dryRun {
						fmt.Printf("  [Dry Run] Would remove %s at %s:%d\n", t.Identifier, t.File, t.Line)
					} else {
						fmt.Printf("  Removed unused %s at %s:%d\n", t.Identifier, t.File, t.Line)
					}
					modified = true
				}
			}
		}

		if modified && !dryRun {
			var buf bytes.Buffer
			if err := format.Node(&buf, fset, f); err != nil {
				fmt.Printf("  Failed to format modified file %s: %v\n", file, err)
				continue
			}
			if err := os.WriteFile(file, buf.Bytes(), 0o600); err != nil {
				fmt.Printf("  Failed to save file %s: %v\n", file, err)
				continue
			}
			changedFilesCount++
		}
	}

	return changedFilesCount
}

// applyRewrite traverses the AST and applies the necessary deletions or replacements.
func applyRewrite(fset *token.FileSet, f *ast.File, t Target) bool {
	modified := false

	// astutil.Apply allows us to safely delete nodes or replace them on the fly
	astutil.Apply(f, nil, func(c *astutil.Cursor) bool {
		n := c.Node()
		if n == nil {
			return true
		}

		switch node := n.(type) {

		// 1. Remove Unused Imports
		case *ast.ImportSpec:
			if t.Kind == TargetImport && fset.Position(node.Pos()).Line == t.Line {
				c.Delete()
				modified = true
			}

		// 2. Remove Unused Variables & Constants (e.g. var a, b = 1, 2)
		case *ast.ValueSpec:
			if t.Kind == TargetDecl {
				idx := -1
				for i, name := range node.Names {
					if name.Name == t.Identifier && fset.Position(name.Pos()).Line == t.Line {
						idx = i
						break
					}
				}
				if idx != -1 {
					modified = true

					// Edge Case: multi-return assign (var a, b = foo())
					if len(node.Values) == 1 && len(node.Names) > 1 {
						node.Names[idx] = ast.NewIdent("_")
						c.Replace(node)
					} else {
						// Filter out the unused var and its corresponding value
						var newNames []*ast.Ident
						var newValues []ast.Expr
						for i, name := range node.Names {
							if i != idx {
								newNames = append(newNames, name)
								if len(node.Values) > i {
									newValues = append(newValues, node.Values[i])
								}
							}
						}

						node.Names = newNames
						if len(node.Values) > 0 {
							node.Values = newValues
						}

						// If no variables remain in this spec, delete the entire spec
						if len(node.Names) == 0 {
							c.Delete()
						} else {
							c.Replace(node)
						}
					}
				}
			}

		// 3. Handle Short Assignments (a, b := foo())
		case *ast.AssignStmt:
			if t.Kind == TargetDecl && node.Tok == token.DEFINE {
				for i, lhs := range node.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name == t.Identifier && fset.Position(ident.Pos()).Line == t.Line {
						// NEVER remove the element; replace it with the blank identifier '_'
						node.Lhs[i] = ast.NewIdent("_")
						modified = true
					}
				}
			}

		// 4. Remove Unused Functions
		case *ast.FuncDecl:
			if t.Kind == TargetDecl && node.Name.Name == t.Identifier && fset.Position(node.Name.Pos()).Line == t.Line {
				c.Delete()
				modified = true
			}

		// 5. Remove Unused Types
		case *ast.TypeSpec:
			if t.Kind == TargetDecl && node.Name.Name == t.Identifier && fset.Position(node.Name.Pos()).Line == t.Line {
				c.Delete()
				modified = true
			}

		// 6. Cleanup Empty Blocks
		case *ast.GenDecl:
			// If all specs inside a declaration (import, var, const, type) were removed, drop it entirely.
			// This prevents dangling 'import ()' or 'var ()' blocks.
			if len(node.Specs) == 0 && (node.Tok == token.IMPORT || node.Tok == token.VAR || node.Tok == token.CONST || node.Tok == token.TYPE) {
				if c.Index() >= 0 {
					c.Delete()
				}
			}

		// 7. Cleanup Empty DeclStmt (e.g. local variables)
		case *ast.DeclStmt:
			if gen, ok := node.Decl.(*ast.GenDecl); ok && len(gen.Specs) == 0 {
				c.Delete()
			}
		}

		return true
	})

	return modified
}

// promptUser handles the --interactive mode.
func promptUser(t Target) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("\nUnused declaration [%s] at %s:%d\n", t.Identifier, t.File, t.Line)

	actionStr := "comment"
	shortStr := "c"
	if removeMode {
		actionStr = "remove"
		shortStr = "r"
	}

	fmt.Printf("Action: [%s]%s, [i]gnore ? (default %s): ", shortStr, actionStr[1:], shortStr)

	action, _ := reader.ReadString('\n')
	action = strings.TrimSpace(strings.ToLower(action))

	return action == "" || action == shortStr || action == actionStr
}

// isGenerated attempts to detect if a file is auto-generated to prevent unsafe modifications.
func isGenerated(file string) bool {
	content, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	head := string(content)
	if len(head) > 1000 {
		head = head[:1000]
	}
	return strings.Contains(head, "Code generated") && strings.Contains(head, "DO NOT EDIT")
}

// isCgo checks if a file relies on CGO, as altering these files is risky.
func isCgo(file string) bool {
	content, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	return strings.Contains(string(content), `import "C"`)
}

// applyCommentText finds the node's bounds via AST and comments out those lines in the raw text.
func applyCommentText(filename string, fset *token.FileSet, f *ast.File, t Target) bool {
	startLine, endLine := -1, -1

	// 1. Find bounds of the node
	astutil.Apply(f, nil, func(c *astutil.Cursor) bool {
		if startLine != -1 {
			return false // Already found
		}
		n := c.Node()
		if n == nil {
			return true
		}

		match := false
		switch node := n.(type) {
		case *ast.ImportSpec:
			match = t.Kind == TargetImport && fset.Position(node.Pos()).Line == t.Line
		case *ast.ValueSpec:
			if t.Kind == TargetDecl {
				for _, name := range node.Names {
					if name.Name == t.Identifier && fset.Position(name.Pos()).Line == t.Line {
						if len(node.Names) > 1 {
							// We can't line-comment a shared var declaration (e.g. var a, b = 1, 2)
							// without breaking the other used variables. Fallback to rewrite.
							return true
						}
						match = true
						break
					}
				}
			}
		case *ast.AssignStmt:
			// Short assignments shouldn't be commented entirely, just let it fall through or handle specially
			if t.Kind == TargetDecl && node.Tok == token.DEFINE {
				for _, lhs := range node.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name == t.Identifier && fset.Position(ident.Pos()).Line == t.Line {
						// We can't line-comment a partial assignment. Fallback to just replacing with '_'
						return true
					}
				}
			}
		case *ast.FuncDecl:
			match = t.Kind == TargetDecl && node.Name.Name == t.Identifier && fset.Position(node.Name.Pos()).Line == t.Line
		case *ast.TypeSpec:
			match = t.Kind == TargetDecl && node.Name.Name == t.Identifier && fset.Position(node.Name.Pos()).Line == t.Line
		}

		if match {
			startLine = fset.Position(n.Pos()).Line
			endLine = fset.Position(n.End()).Line
			return false
		}
		return true
	})

	if startLine == -1 {
		// Fallback: If it's a short assignment (like `a, b := foo()`), applying AST rewrite to `_` is the only valid way to "comment" it.
		modified := applyRewrite(fset, f, t)
		if modified && !dryRun {
			var buf bytes.Buffer
			if err := format.Node(&buf, fset, f); err != nil {
				return false
			}
			if err := os.WriteFile(filename, buf.Bytes(), 0o600); err != nil {
				return false
			}
		}
		return modified
	}

	if dryRun {
		return true
	}

	// 2. Read lines and comment out
	content, err := os.ReadFile(filename)
	if err != nil {
		return false
	}

	lines := strings.Split(string(content), "\n")

	// Convert 1-based AST lines to 0-based slice indices
	for i := startLine - 1; i <= endLine-1 && i < len(lines); i++ {
		lines[i] = "// " + lines[i]
	}

	err = os.WriteFile(filename, []byte(strings.Join(lines, "\n")), 0o600)
	return err == nil
}
