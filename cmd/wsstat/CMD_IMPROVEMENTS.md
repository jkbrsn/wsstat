# cmd/wsstat Package Improvement Suggestions

This document outlines structural and organizational improvements for the `cmd/wsstat` package based on analysis conducted 2025-10-02.

## Current Structure

```
cmd/wsstat/
├── main.go              # Entry point, CLI orchestration, business logic
├── flags.go             # Custom flag.Value implementations
├── main_helpers.go      # Parsing, validation, preprocessing
└── main_test.go         # Single test (TestResolveCountValue)
```

## Structural Issues

### 1. Overly Complex Flag Handling

- `main.go:110-185` contains business logic mixed with CLI orchestration
- Custom flag types (`trackedIntFlag`, `verbosityCounter`) in `flags.go` are clever but add complexity
- `preprocessVerbosityArgs()` rewrites `os.Args` globally—fragile and hard to test

### 2. File Organization

- `main_helpers.go` is a vague catch-all name (violates Go's preference for specific naming)
- Functions like `parseValidateInput()`, `resolveCountValue()`, and `parseWSURI()` belong together but spread across files
- Only one test in `main_test.go` (`TestResolveCountValue`)—most logic untested

### 3. Separation of Concerns

- `main()` directly calls `app.NewClient()` then chains validation, execution, and output
- Error handling is repetitive (8 similar `fmt.Printf` + `os.Exit(1)` blocks in main.go:113-184)
- Context creation duplicated three times (main.go:142, 153, 163)

### 4. Testability

- `parseValidateInput()` calls `os.Exit(0)` for `-version` (main_helpers.go:50)—prevents testing
- `preprocessVerbosityArgs()` mutates `os.Args` globally—not parallelizable
- No tests for URL parsing, header validation, or the main execution paths

## Specific Improvements

### A. Extract a Config Type

Replace scattered flags with a single `Config` struct built from parsed flags:

```go
type Config struct {
    TargetURL       *url.URL
    Count           int
    Headers         []string
    RPCMethod       string
    TextMessage     string
    Subscribe       bool
    SubscribeOnce   bool
    BufferSize      int
    SummaryInterval time.Duration
    Format          string
    ColorMode       string
    Quiet           bool
    Verbosity       int
}

func parseConfig() (*Config, error) { /* ... */ }
```

**Benefits:**
- Single validation point
- Easier to test
- Clearer contract between parsing and execution

### B. Introduce a `run()` Function

Move all logic from `main()` into `func run(cfg *Config) error` that returns errors instead of calling `os.Exit()`:

```go
func main() {
    if err := run(); err != nil {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        os.Exit(1)
    }
}

func run() error {
    cfg, err := parseConfig()
    if err != nil {
        return fmt.Errorf("parsing config: %w", err)
    }

    ws := app.NewClient(/* options from cfg */)

    if err := ws.Validate(); err != nil {
        return fmt.Errorf("invalid settings: %w", err)
    }

    // Execute based on mode
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    // ... rest of execution logic

    return nil
}
```

**Benefits:**
- Testable without `os.Exit()` interference
- Single exit point in `main()`
- Cleaner error handling with wrapping

### C. Consolidate Helper Functions

Rename `main_helpers.go` → `config.go` and group related functions:

- `parseConfig()` (combines `parseValidateInput` + `resolveCountValue`)
- `validateConfig()` (business rule checks)
- `parseWSURI()` (already fits)

Move flag type definitions to a separate `flags.go` or inline into `config.go`.

**Proposed structure:**

```
cmd/wsstat/
├── main.go          # Entry point only: calls run(), handles os.Exit()
├── config.go        # Config type, parseConfig(), validateConfig(), parseWSURI()
├── flags.go         # Custom flag.Value types (or merge into config.go)
└── *_test.go        # Comprehensive tests for all parsing/validation logic
```

### D. Simplify Verbosity Handling

Replace `preprocessVerbosityArgs()` with a post-parse approach:

```go
func parseVerbosity() int {
    count := 0
    flag.Visit(func(f *flag.Flag) {
        if f.Name == "v" {
            if val, _ := strconv.Atoi(f.Value.String()); val > 0 {
                count = val
            }
        }
    })
    return count
}
```

Or accept that `-v -v` requires users to write `-v=2` (standard Go flag behavior).

**Benefits:**
- No global `os.Args` mutation
- Parallelizable tests
- Simpler mental model

### E. Add Unit Tests

Current test coverage gaps:

- `parseWSURI()` with/without schemes, invalid URLs
- `parseValidateInput()` error cases (mutual exclusions, missing args)
- `headerList.Set()` with empty/whitespace values
- `verbosityCounter` edge cases
- `resolveCountValue()` (only test currently present)
- Config validation rules

**Recommended test files:**

```
cmd/wsstat/
├── config_test.go       # parseConfig, validateConfig, parseWSURI
├── flags_test.go        # Custom flag.Value implementations
└── main_test.go         # Integration test for run() if extracted
```

### F. Reduce Duplication in `main()`

Extract shared context + error handling:

```go
func runWithContext(fn func(context.Context) error) error {
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()
    return fn(ctx)
}

// Usage:
if cfg.SubscribeOnce {
    return runWithContext(func(ctx context.Context) error {
        return ws.StreamSubscriptionOnce(ctx, cfg.TargetURL)
    })
}
```

**Benefits:**
- DRY principle
- Consistent context/cancellation handling

### G. Minor Cleanups

1. **Comment typo** in `flags.go:13`: `headerList.Set()` comment says "converts to integer" (copy-paste error from `trackedIntFlag`)
2. **Inline `onlyRune()`**: Used only for `-vv` parsing in `preprocessVerbosityArgs()`—consider inlining if that function is simplified
3. **Usage formatting**: Manual string building in `main.go:54-105` is verbose—consider table-driven approach or a library like `spf13/cobra` for future enhancements

## Priority Ranking

### High Priority

1. **Extract `Config` type + `run()` function** (main.go)
   - Enables testing
   - Clarifies contract between parsing and execution
   - Single responsibility: `main()` handles exit, `run()` handles logic

2. **Rename `main_helpers.go` → `config.go`** and consolidate logic
   - Groups related functionality
   - Clearer naming
   - Better discoverability

### Medium Priority

3. **Add tests for parsing/validation logic**
   - Critical for refactoring confidence
   - Catches edge cases early
   - Documents expected behavior

4. **Simplify or remove `preprocessVerbosityArgs()`**
   - Reduces global state mutation
   - Improves testability
   - Alternative: document `-v=N` syntax in usage

### Low Priority

5. **DRY up context creation and error handling** in `main()`
   - Quality of life improvement
   - Easier to maintain

6. **Fix comment typo, inline `onlyRune()`** if not reused
   - Code hygiene
   - Minimal impact

## Benefits of These Changes

This refactoring would align the package with Go best practices:

- **Testability**: Pure functions, no `os.Exit()` in testable code, no global mutation
- **Clear data flow**: Config → Validation → Execution → Output
- **Minimal global state**: Flags parsed once into config struct
- **Better error handling**: Wrapped errors with context, single exit point
- **Maintainability**: Related functions grouped by concern, clear file names

## Migration Path

1. Create `config.go` with `Config` type and `parseConfig()` (keep existing functions initially)
2. Extract `run()` function that returns errors
3. Add tests for `parseConfig()` and validation
4. Gradually migrate logic from `main_helpers.go` to `config.go`
5. Simplify or remove `preprocessVerbosityArgs()` once tests are in place
6. Delete `main_helpers.go` when empty
7. Refactor duplicate context/error handling

Each step can be a separate commit, maintaining working state throughout.
