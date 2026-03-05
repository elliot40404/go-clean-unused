# go-clean-unused

A smart CLI tool that automatically comments out or removes unused Go declarations (variables, imports, constants, functions, and types) so your project can compile successfully.

If you have ever been frustrated by Go's strict "declared and not used" or "imported and not used" compiler errors while rapidly prototyping, debugging, or performing large refactors, this tool is for you.

## Features

- **Safe by Default**: Defaults to commenting out unused code rather than deleting it.
- **AST-Aware**: Uses Go's native `go/ast` and `go/packages`. It doesn't rely on fragile text/regex replacement.
- **Smart Edge-Case Handling**:
  - Safely handles shared declarations (e.g., converts `var a, b = 1, 2` to `var b = 2` if `a` is unused)
  - Handles short variable assignments (e.g., converts `a, b := foo()` to `_, b := foo()`)
  - Cleans up empty import `()` or `var ()` blocks
- **Iterative Resolution**: Automatically runs multiple passes to catch cascading unused variables (e.g., when removing a function makes an import unused)
- **Interactive & Dry-Run Modes**: See exactly what will happen before touching your files, or decide on a case-by-case basis

## Installation

```bash
go install github.com/elliot40404/go-clean-unused@latest
```

## Usage

Run the tool at the root of your Go module:

```bash
go-clean-unused ./...
```

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--remove` | false | Remove unused code entirely instead of commenting it out. |
| `--interactive` | false | Prompt [c]omment/[r]emove or [i]gnore before modifying each unused declaration. |
| `--dry-run` | false | Print proposed changes to the console but do not modify any files on disk. |
| `--max-passes` | 10 | Maximum number of build/fix iterations to resolve cascading errors. |
| `--include-tests` | true | Include _test.go files in the analysis. |

### Examples

1. Comment out all unused code in the current project (Default)

```bash
go-clean-unused ./...
```

2. Preview what would be deleted without changing files

```bash
go-clean-unused --remove --dry-run ./...
```

3. Manually approve every fix

```bash
go-clean-unused --interactive ./...
```

## How It Works

`go-clean-unused` does not attempt to be a full dead-code static analyzer (like staticcheck). Instead, it leverages the Go compiler itself:

1. It loads your packages using `golang.org/x/tools/go/packages` to capture exact, structurally-typed compiler errors.
2. It maps the reported "declared and not used" errors to their exact AST nodes.
3. It performs surgical source-code edits (either by commenting out the lines or rewriting the AST using `golang.org/x/tools/go/ast/astutil`).
4. It formats the output via `go/format` and repeats the process until the compiler is happy.

## Safety Guards

To prevent destructive behavior, `go-clean-unused`:

- **Never removes exported identifiers** (e.g., `func PublicAPI()`)
- **Skips auto-generated files** (files containing `Code generated ... DO NOT EDIT`)
- **Skips files relying on CGO** (import `"C"`)

## Contributing

Pull requests are welcome! To run the test suite locally:

```bash
go test -v ./...
```

## License

MIT License - see [LICENSE](LICENSE) file for details.
