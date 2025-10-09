# CLI Help Output Improvements

## Overview

This document outlines improvements to `wsstat`'s help output, inspired by best practices from `websocat` while maintaining wsstat's focused design philosophy.

## Technical Decisions

### Go Flag Handling
- **Approach**: Use standard library `flag` package with double registration pattern
- **Implementation**: Register same variable twice with different names (e.g., `flag.BoolVar(&subscribe, "s", ...)` and `flag.BoolVar(&subscribe, "subscribe", ...)`)
- **Precedent**: Already used in wsstat for `-H`/`-header` (main.go:76-77); also used by FiloSottile's `age` tool
- **Custom help**: Completely replace `flag.Usage` with custom function (abandon `flag.PrintDefaults()`)
- **Existing structure**: `init()` function exists at main.go:74-132 containing flag.Usage custom function
- **Repeatable flags**: Implemented via custom `flag.Value` type (`headerList` in flags.go:9-33)

### Double-Dash Convention
- **Decision**: Display `--flag` in help text but register as `flag` (single dash)
- **Rationale**: Go's `flag` package treats `-flag` and `--flag` identically, so help shows `--` for long forms as visual convention while accepting both in practice
- **Example**: Help shows `-t, --text <string>` but both are registered as `"text"` and `"t"`

### Version Handling
- **Confirmed**: `version` variable exists in main.go:62, defaults to "unknown"
- **Build-time**: Set via `-ldflags` during build (current: "2.0.0" from VERSION file)
- **Display**: Show in header as `wsstat <version>`

### Column Width
- **Updated**: 35 characters for flag column (increased from initial 30)
- **Reason**: Longest flag `--summary-interval <duration>` is ~32 chars; 35 provides safety margin
- **Format**: Description text starts at column 36

### -k/--insecure Flag
- **Scope**: Add flag definition and help text only
- **TLS wiring**: Out of scope for this implementation (add TODO comment)
- **Future**: Backend TLS config integration is separate task

### Short Forms
- **Selective**: Not all flags receive short forms (see table in section 4)
- **Criteria**: Common flags and industry conventions (curl/websocat compatibility)
- **Long-only flags**: `--rpc-method`, `--subscribe-once`, `--no-tls`, `--color`, `--version`, `--summary-interval`

## Goals

1. Add version/description header for clear tool identity
2. Show both short and long flag forms together for discoverability
3. Inline defaults consistently across all options
4. Distinguish boolean flags from value-taking options through formatting
5. Add usage patterns at the top for quick orientation
6. Include security warnings for sensitive flags
7. Add `-k, --insecure` flag for TLS verification bypass
8. Keep examples section at bottom (preserve existing examples; see section 9)

---

## Current Structure

```
Usage:  wsstat [options] <url>

Measure WebSocket latency or stream subscription events.
If the URL omits a scheme, wsstat assumes wss:// unless -no-tls is provided.

General:
  -count int           number of interactions to perform; 0 means unlimited when subscribing (default 1; defaults to unlimited when subscribing)

Input (choose one):
  -rpc-method string   JSON-RPC method name to send (id=1, jsonrpc=2.0)
  -text string         text message to send
...
```

**Issues:**
- No version/author information
- Inconsistent flag format (some show short form, others don't)
- Defaults sometimes inline, sometimes not
- Hard to distinguish boolean flags from value-taking options

---

## Proposed Structure

```
wsstat <version>
Measure WebSocket latency and stream subscription performance

USAGE:
  wsstat [options] <url>                  (measure latency)
  wsstat -subscribe [options] <url>       (stream subscription)

Note: Flags without <type> are boolean switches. Flags with <type> require a value.

General:
  -c, --count <int>              number of interactions [default: 1; unlimited when subscribing]
      --version                  print program version and exit

Input (choose one):
      --rpc-method <string>      JSON-RPC method name to send (id=1, jsonrpc=2.0)
  -t, --text <string>            text message to send

Subscription:
  -s, --subscribe                stream events until interrupted
      --subscribe-once           subscribe and exit after the first event
  -b, --buffer <int>             subscription delivery buffer size in messages [default: 0]
      --summary-interval <duration>
                                 print subscription summaries every interval (e.g., 1s, 5m, 1h) [default: disabled]

Connection:
  -H, --header <string>          HTTP header to include with request (repeatable; format: "Key: Value")
  -k, --insecure                 skip TLS certificate verification (use with caution)
      --no-tls                   assume ws:// when URL lacks scheme [default: wss://]
      --color <string>           color output mode: auto, always, never [default: auto]

Output:
  -q, --quiet                    suppress all output except response
  -v, --verbose                  increase verbosity (level 1)
  -vv                            increase verbosity (level 2)
  -f, --format <string>          output format: auto, json, raw [default: auto]

Verbosity Levels:
  (default)                      minimal request info with summary timings
  -v                             adds target/TLS summaries and timing diagram
  -vv                            includes full TLS certificates and headers

Security Notes:
  -k, --insecure: Disables TLS certificate verification. Only use for testing or when
                  connecting to endpoints with self-signed certificates. This makes
                  your connection vulnerable to MITM attacks.

Examples:
  wsstat wss://echo.example.com
  wsstat -text "ping" wss://echo.example.com
  wsstat --rpc-method eth_blockNumber wss://rpc.example.com/ws
  wsstat -subscribe -count 1 wss://stream.example.com/feed
  wsstat -subscribe --summary-interval 5s wss://stream.example.com/feed
  wsstat -H "Authorization: Bearer TOKEN" -H "Origin: https://foo" wss://api.example.com/ws
  wsstat -k wss://self-signed.example.com
```

---

## Detailed Changes

### 1. Version Header

**Implementation:**
- Add at top of help output
- Pull from existing `version` variable (confirmed at main.go:62, set via ldflags at build time)
- Format: `wsstat <version>` on first line
- One-line description on second line

```go
fmt.Fprintf(os.Stderr, "wsstat %s\n", version)
fmt.Fprintln(os.Stderr, "Measure WebSocket latency and stream subscription performance")
fmt.Fprintln(os.Stderr)
```

### 2. Usage Patterns

**Implementation:**
- Add "USAGE:" section immediately after header
- Show two primary usage modes (latency measurement vs subscription streaming)
- Keep concise, 1-2 lines

```go
fmt.Fprintln(os.Stderr, "USAGE:")
fmt.Fprintln(os.Stderr, "  wsstat [options] <url>                  (measure latency)")
fmt.Fprintln(os.Stderr, "  wsstat -subscribe [options] <url>       (stream subscription)")
fmt.Fprintln(os.Stderr)
```

### 3. Boolean vs Value Distinction

**Implementation:**
- Add explanatory note after USAGE section
- Use consistent formatting:
  - Boolean flags: `-f, --flag` (no type marker)
  - Value flags: `-f, --flag <type>` (with type marker)
- Type markers: `<string>`, `<int>`, `<duration>`, etc.

```go
fmt.Fprintln(os.Stderr, "Note: Flags without <type> are boolean switches. Flags with <type> require a value.")
fmt.Fprintln(os.Stderr)
```

### 4. Short + Long Forms

**Current flags to update:**

| Current | Proposed Short | Proposed Long | Type |
|---------|---------------|---------------|------|
| `-count` | `-c` | `--count` | `<int>` |
| `-text` | `-t` | `--text` | `<string>` |
| `-rpc-method` | (none) | `--rpc-method` | `<string>` |
| `-subscribe` | `-s` | `--subscribe` | boolean |
| `-subscribe-once` | (none) | `--subscribe-once` | boolean |
| `-buffer` | `-b` | `--buffer` | `<int>` |
| `-summary-interval` | (none) | `--summary-interval` | `<duration>` |
| `-H` / `-header` | `-H` | `--header` | `<string>` |
| `-no-tls` | (none) | `--no-tls` | boolean |
| `-color` | (none) | `--color` | `<string>` |
| `-q` | `-q` | `--quiet` | boolean |
| `-v` | `-v` | `--verbose` | boolean |
| `-vv` | `-vv` | (none) | boolean |
| `-format` | `-f` | `--format` | `<string>` |
| `-version` | (none) | `--version` | boolean |
| **NEW:** `-insecure` | `-k` | `--insecure` | boolean |

**Rationale for short forms:**
- `-c`: count (common convention)
- `-t`: text (common in tools like curl)
- `-s`: subscribe (natural mnemonic)
- `-b`: buffer (natural mnemonic)
- `-H`: header (matches curl exactly)
- `-q`: quiet (ubiquitous convention)
- `-v`: verbose (ubiquitous convention)
- `-f`: format (common convention)
- `-k`: insecure (matches curl and websocat)

**Implementation:**
```go
// Add short form aliases to existing init() function (main.go:74-132)
// Note: init() already exists and contains double registration for -H/-header
flag.BoolVar(subscribe, "s", false, "stream events until interrupted")
flag.IntVar(bufferSize, "b", 0, "subscription delivery buffer size in messages")
flag.StringVar(textMessage, "t", "", "text message to send")
flag.StringVar(formatOption, "f", "auto", "output format: auto, json, raw")
// -q already defined at main.go:69
// -v and -vv already defined at main.go:70-71
// ... etc
```

### 5. Inline Defaults

**Format:** `[default: value]` or `[default: disabled]` for zero-values that mean "off"

**Examples:**
- `-c, --count <int>              number of interactions [default: 1; unlimited when subscribing]`
- `-b, --buffer <int>             subscription delivery buffer size in messages [default: 0]`
- `-f, --format <string>          output format: auto, json, raw [default: auto]`
- `    --color <string>           color output mode: auto, always, never [default: auto]`
- `    --summary-interval <duration>  print subscription summaries every interval [default: disabled]`

**Implementation:**
```go
// Use consistent formatting in Usage function
fmt.Fprintf(os.Stderr, "  -c, --count <int>              %s [default: 1; unlimited when subscribing]\n",
    "number of interactions")
fmt.Fprintf(os.Stderr, "  -b, --buffer <int>             %s [default: %d]\n",
    "subscription delivery buffer size in messages", *bufferSize)
```

### 6. Add -k, --insecure Flag

**Purpose:** Skip TLS certificate verification (for self-signed certs, testing)

**Implementation notes:**
- Add flag definition in main.go
- Leave as stub with TODO comment (TLS wiring is separate task)
- Add to "Connection:" section of help
- Include security warning in dedicated section

```go
// In main.go var block (add after line 66, in "Connection behavior" section):
insecure = flag.Bool("insecure", false, "skip TLS certificate verification")
// TODO: Wire -k/--insecure through config.go to TLS config (tls.Config.InsecureSkipVerify)

// In existing init() function (add after headerArguments registration at line 77):
flag.BoolVar(insecure, "k", false, "skip TLS certificate verification")

// In help output under Connection:
fmt.Fprintln(os.Stderr, "Connection:")
fmt.Fprintf(os.Stderr, "  -H, --header <string>          %s\n", "HTTP header to include (repeatable)")
fmt.Fprintf(os.Stderr, "  -k, --insecure                 %s\n", "skip TLS certificate verification (use with caution)")
fmt.Fprintf(os.Stderr, "      --no-tls                   %s [default: wss://]\n", "assume ws:// when URL lacks scheme")
```

**Scope:** This implementation adds the flag definition and help text only. Backend TLS config wiring is deferred as a separate task.

### 7. Security Warnings Section

**Implementation:**
Add new section after "Verbosity Levels:" and before "Examples:"

```go
fmt.Fprintln(os.Stderr, "Security Notes:")
fmt.Fprintln(os.Stderr, "  -k, --insecure: Disables TLS certificate verification. Only use for testing or when")
fmt.Fprintln(os.Stderr, "                  connecting to endpoints with self-signed certificates. This makes")
fmt.Fprintln(os.Stderr, "                  your connection vulnerable to MITM attacks.")
fmt.Fprintln(os.Stderr)
```

### 8. Formatting Standards

**Alignment:**
- Flag column: 35 characters wide (accommodates longest flag + type)
- Description starts at column 36
- Multi-line descriptions indent to column 36

**Example:**
```
  -c, --count <int>              number of interactions [default: 1]
      --summary-interval <duration>
                                 print subscription summaries [default: disabled]
```

**Type markers:**
- `<int>` for integers
- `<string>` for strings
- `<duration>` for Go duration strings (e.g., "5s", "1m", "1h30m")

### 9. Examples Section

**Implementation:**
- **MUST** appear at the very end of help output (confirmed: currently at main.go:124-130)
- Preserve existing examples exactly as written (they are already excellent)
- Add new example for `-k/--insecure` flag: `wsstat -k wss://self-signed.example.com`
- No other changes to examples content

```go
// At the end of flag.Usage function, after Security Notes:
fmt.Fprintln(os.Stderr)
fmt.Fprintln(os.Stderr, "Examples:")
fmt.Fprintln(os.Stderr, "  wsstat wss://echo.example.com")
fmt.Fprintln(os.Stderr, "  wsstat -text \"ping\" wss://echo.example.com")
fmt.Fprintln(os.Stderr, "  wsstat -rpc-method eth_blockNumber wss://rpc.example.com/ws")
fmt.Fprintln(os.Stderr, "  wsstat -subscribe -count 1 wss://stream.example.com/feed")
fmt.Fprintln(os.Stderr, "  wsstat -subscribe -summary-interval 5s wss://stream.example.com/feed")
fmt.Fprintln(os.Stderr, "  wsstat -H \"Authorization: Bearer TOKEN\" -H \"Origin: https://foo\" wss://api.example.com/ws")
fmt.Fprintln(os.Stderr, "  wsstat -k wss://self-signed.example.com")
```

---

## Implementation Checklist

### Phase 1: Structure and Format
- [ ] Add version header (pull from `version` variable)
- [ ] Add one-line description
- [ ] Add USAGE section with two patterns
- [ ] Add explanatory note about boolean vs value flags

### Phase 2: Flag Updates
- [ ] Add short forms to existing flags (see table above)
- [ ] Update flag registration in `init()`
- [ ] Ensure both forms point to same variable

### Phase 3: Help Text Formatting
- [ ] Update all flag descriptions to include type markers (`<int>`, `<string>`, etc.)
- [ ] Inline defaults consistently using `[default: X]` format
- [ ] Align all descriptions to column 36
- [ ] Multi-line descriptions indent properly

### Phase 4: New Flag
- [ ] Add `insecure` boolean variable
- [ ] Register with both `-k` and `--insecure`
- [ ] Add to "Connection:" section in help
- [ ] Add TODO comment for future TLS config wiring (stub implementation)

### Phase 5: Security Section
- [ ] Add "Security Notes:" section after "Verbosity Levels:"
- [ ] Add warning text for `-k, --insecure`
- [ ] Consider adding notes for other security-relevant flags if needed

### Phase 6: Examples Section
- [ ] Preserve existing examples at end of help output (main.go:124-130)
- [ ] Add new example for `-k/--insecure` flag
- [ ] Ensure examples appear after Security Notes section

### Phase 7: Testing
- [ ] Run `go run ./cmd/wsstat --help` and verify formatting
- [ ] Check alignment with different terminal widths
- [ ] Verify all short forms work: `go run ./cmd/wsstat -h`, `-v`, `-q`, `-s`, etc.
- [ ] Ensure `--help` still works (if implemented)
- [ ] Confirm examples appear at the very end

---

## Example Before/After Comparison

### Before (Current)
```
Connection:
  -H / -header string  HTTP header to include with the request (repeatable; format: Key: Value)
  -no-tls              assume ws:// when input URL lacks scheme (default wss://)
  -color string        color output: auto, always, or never (auto|always|never; default "auto")
```

### After (Proposed)
```
Connection:
  -H, --header <string>          HTTP header to include with request (repeatable; format: "Key: Value")
  -k, --insecure                 skip TLS certificate verification (use with caution)
      --no-tls                   assume ws:// when URL lacks scheme [default: wss://]
      --color <string>           color output mode: auto, always, never [default: auto]
```

**Key improvements:**
- Consistent short/long form presentation
- Type markers make value requirements explicit
- Inline defaults in consistent format
- Boolean flags immediately identifiable (no type marker)
- New `-k` flag for common use case

---

## Design Rationale

### Why Not Separate FLAGS/OPTIONS Sections?

**Decision:** Use functional grouping (Input, Subscription, Connection, Output) rather than type-based separation (FLAGS vs OPTIONS).

**Reasoning:**
1. **User mental model:** Users think in terms of "what they want to do" (send a message, subscribe, configure connection) rather than "what type of argument this flag takes"
2. **wsstat is focused:** Unlike websocat (which is a swiss-army knife with ~50 flags), wsstat has ~15 flags organized into clear use cases
3. **Type markers solve the problem:** Adding `<type>` markers and the explanatory note makes boolean vs value distinction clear without reorganizing
4. **Precedent:** Many focused CLI tools (curl, jq, etc.) group by function, not by flag type

### Why These Short Forms?

Short forms chosen based on:
1. **Industry convention:** `-H` (curl), `-v` (ubiquitous), `-q` (ubiquitous), `-k` (curl/websocat)
2. **Mnemonic strength:** `-s` (subscribe), `-b` (buffer), `-t` (text), `-f` (format), `-c` (count)
3. **Avoiding conflicts:** No duplicates, no ambiguous mappings

### Why Add -k/--insecure?

**Use cases:**
- Development/testing with self-signed certs
- Internal tools connecting to corporate infrastructure
- Debugging TLS issues

**Precedent:**
- curl: `-k, --insecure`
- websocat: `-k, --insecure`
- wget: `--no-check-certificate`

**Decision:** Follow curl/websocat convention with `-k` short form.

---

## Future Considerations

### Tiered Help (Deferred)

If wsstat's feature set grows significantly (e.g., adds server mode, proxying, multiple protocols), consider:
- `--help` or `-h`: Current help (most common flags)
- `--help=long`: All flags including advanced/rare options
- `--help=doc`: Include detailed examples and tutorial content

**Trigger:** When flag count exceeds ~25-30 or when "advanced" flags would confuse typical users.

### Localization (Not Planned)

No current plans for i18n, but if needed:
- Extract help text to separate package/file
- Use message catalog (golang.org/x/text)
- Keep format specifiers and examples locale-aware

---

## Testing Strategy

1. **Visual inspection:** Run help and verify alignment, clarity, completeness
2. **Functional testing:** Ensure all short forms work as expected
3. **Edge cases:** Test with narrow terminal widths (80 chars)
4. **Comparison:** Side-by-side with websocat, curl to validate UX improvements
5. **User feedback:** Gather feedback from existing users on clarity

---

## Maintenance Notes

### Keeping Help in Sync

When adding new flags in future:
1. Add both short and long forms (unless long-only is appropriate)
2. Include type marker for value-taking flags
3. Add inline default if non-zero/non-empty
4. Place in appropriate functional section
5. Update examples if flag enables new use case
6. Add to security notes if security-relevant

### Alignment Updates

If maximum flag length changes:
1. Update column width constant (currently 35)
2. Re-align all descriptions
3. Test with `go run ./cmd/wsstat --help | cat` to verify

---

## Summary

These improvements balance **discoverability** (short forms, clear types, usage patterns) with **focus** (functional grouping, no feature bloat). The result should feel familiar to users of curl/websocat while maintaining wsstat's clarity and purpose-driven design.
