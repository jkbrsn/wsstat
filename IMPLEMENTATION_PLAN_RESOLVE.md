# Implementation Plan: --resolve Flag

## Overview
Add a `--resolve` flag similar to curl's `--resolve` option, allowing users to override DNS resolution for specific host:port combinations. This is useful for testing endpoints with custom IP addresses without modifying `/etc/hosts` or DNS settings.

## Usage Example
```bash
# Connect to localhost:8080 instead of performing DNS lookup for echo.example.com:443
wsstat --resolve echo.example.com:443:127.0.0.1 wss://echo.example.com/ws

# Multiple overrides (flag can be repeated)
wsstat --resolve api.example.com:443:192.168.1.100 \
       --resolve api.example.com:80:192.168.1.101 \
       wss://api.example.com/ws
```

## Architecture Analysis

### Current Flow
1. **CLI Layer** (`cmd/wsstat/`)
   - `main.go`: Entry point, calls `parseConfig()` and creates `app.Client`
   - `config.go`: Parses flags into `Config` struct
   - `flags.go`: Custom flag types (`headerList`, `trackedIntFlag`)

2. **Application Layer** (`internal/app/`)
   - `client.go`: `Client` with functional options
   - `wsstatOptions()` method builds `wsstat.Option` slice
   - Passes options to `wsstat.New()`

3. **Core Layer** (`wsstat.go`)
   - `WSStat` struct with `dialer *websocket.Dialer`
   - `newDialer()`: Creates custom dialer with `NetDialContext`/`NetDialTLSContext`
   - `resolveDialTargets()`: Performs DNS lookup via `net.DefaultResolver.LookupHost()`
   - `dialWithAddresses()`: Connects to resolved IPs

### Modification Points
- **CLI**: Add `--resolve` flag parsing
- **Config**: Store parsed overrides
- **app.Client**: Pass overrides to wsstat layer
- **wsstat.WSStat**: Accept DNS override option
- **resolveDialTargets**: Check overrides before DNS lookup

---

## Implementation Steps

### Step 1: Add Custom Flag Type for DNS Overrides
**File**: `cmd/wsstat/flags.go`

**Tasks**:
- Create `resolveList` type implementing `flag.Value` interface
- Parse format: `host:port:address` (e.g., `example.com:443:127.0.0.1`)
- Support IPv4 and IPv6 addresses
- Store as map key `host:port` → value `address`
- Implement validation:
  - Ensure exactly 3 colon-separated parts
  - Validate port is numeric and in valid range (1-65535)
  - Validate IP address format

**Acceptance Criteria**:
- Parse valid `host:port:address` formats
- Reject invalid formats with clear error messages
- Support multiple `-resolve` flags (additive)
- Handle both IPv4 (`192.168.1.1`) and IPv6 (`::1`, `2001:db8::1`) addresses

---

### Step 2: Add CLI Flag and Config Field
**Files**: `cmd/wsstat/main.go`, `cmd/wsstat/config.go`

**Tasks**:
**In `main.go`**:
- Add global variable: `resolveOverrides resolveList`
- Register flag in `init()`:
  ```go
  flag.Var(&resolveOverrides, "resolve", "resolve host:port to address (format: HOST:PORT:ADDRESS)")
  ```
- Add usage documentation in `printUsage()`

**In `config.go`**:
- Add field to `Config` struct:
  ```go
  Resolves map[string]string // key: "host:port", value: "address"
  ```
- In `parseConfig()`, populate `cfg.Resolves` from `resolveOverrides.Values()`

**Acceptance Criteria**:
- `--resolve` flag appears in `wsstat --help`
- Invalid format shows clear error message
- Multiple `--resolve` flags work correctly

---

### Step 3: Add app.Client Option
**File**: `internal/app/client.go`

**Tasks**:
- Add field to `Client` struct:
  ```go
  resolves map[string]string // DNS override map
  ```
- Create option function:
  ```go
  // WithResolves sets DNS resolution overrides for specific host:port combinations.
  func WithResolves(resolves map[string]string) Option {
      return func(c *Client) { c.resolves = resolves }
  }
  ```
- Update `wsstatOptions()` to pass resolves to wsstat layer

**In `cmd/wsstat/main.go`**:
- Add `app.WithResolves(cfg.Resolves)` when creating client

**Acceptance Criteria**:
- Config flows from CLI → app.Client
- No breaking changes to existing API

---

### Step 4: Add wsstat Package Option
**File**: `wsstat.go`

**Tasks**:
- Update `options` struct:
  ```go
  type options struct {
      tlsConfig  *tls.Config
      timeout    time.Duration
      bufferSize int
      logger     zerolog.Logger
      resolves   map[string]string  // NEW
  }
  ```
- Create public option:
  ```go
  // WithResolves sets DNS resolution overrides.
  // Map key format: "host:port", value: "ip_address"
  func WithResolves(resolves map[string]string) Option {
      return func(o *options) { o.resolves = resolves }
  }
  ```
- Store in `WSStat` struct:
  ```go
  type WSStat struct {
      // ... existing fields ...
      resolves map[string]string  // NEW
  }
  ```
- Initialize in `New()`:
  ```go
  ws := &WSStat{
      // ... existing fields ...
      resolves: cfg.resolves,
  }
  ```
- Pass to `newDialer()` function signature

**Acceptance Criteria**:
- Option pattern follows existing conventions
- Thread-safe (read-only after initialization)

---

### Step 5: Modify DNS Resolution Logic
**File**: `wsstat.go`

**Tasks**:
Update `resolveDialTargets()`:
```go
func resolveDialTargets(
    ctx context.Context,
    addr string,
    timings *wsTimings,
    resolves map[string]string,  // NEW parameter
) (dialTarget, error) {
    host, port, err := net.SplitHostPort(addr)
    if err != nil {
        return dialTarget{}, err
    }

    // Check for DNS override FIRST
    key := net.JoinHostPort(host, port)
    if overrideIP, ok := resolves[key]; ok {
        timings.dnsLookupDone = time.Now()
        return dialTarget{
            host:  host,
            port:  port,
            addrs: []string{overrideIP},
        }, nil
    }

    // Fall back to DNS lookup
    addrs, err := net.DefaultResolver.LookupHost(ctx, host)
    if err != nil {
        return dialTarget{}, err
    }

    timings.dnsLookupDone = time.Now()
    if len(addrs) == 0 {
        return dialTarget{}, fmt.Errorf("no addresses found for %s", host)
    }

    return dialTarget{host: host, port: port, addrs: addrs}, nil
}
```

Update `newDialer()` to accept and pass `resolves` map:
- Capture `resolves` from options
- Pass to both `NetDialContext` and `NetDialTLSContext` closures
- Each closure calls updated `resolveDialTargets(ctx, addr, timings, resolves)`

**Acceptance Criteria**:
- Override takes precedence over DNS
- DNS lookup only happens if no override exists
- Timing measurements remain accurate (dnsLookupDone set even for overrides)
- Original behavior unchanged when no overrides provided

---

### Step 6: Add Unit Tests
**File**: `wsstat_test.go`

**Test Cases**:
1. **TestResolveOverride_Basic**
   - Create WSStat with single override
   - Dial target matching override
   - Verify connection uses override IP, not DNS

2. **TestResolveOverride_MultipleHosts**
   - Multiple overrides for different hosts
   - Verify each resolves correctly

3. **TestResolveOverride_MultiplePortsSameHost**
   - Override `example.com:443` → `192.168.1.1`
   - Override `example.com:80` → `192.168.1.2`
   - Verify port-specific routing

4. **TestResolveOverride_FallbackToDNS**
   - Override for `example.com:443`
   - Connect to `other.com:443` (no override)
   - Verify DNS lookup happens for non-overridden host

5. **TestResolveOverride_IPv6**
   - Override with IPv6 address (`::1`, `2001:db8::1`)
   - Verify IPv6 connection works

6. **TestResolveOverride_InvalidIP**
   - Override with malformed IP at dial time
   - Verify connection fails with appropriate error

**File**: `cmd/wsstat/flags_test.go`

**Test Cases**:
1. **TestResolveList_ParseValid**
   - Parse `example.com:443:192.168.1.1`
   - Parse IPv6: `example.com:443:2001:db8::1`
   - Parse IPv6 bracket notation: `example.com:443:[::1]`

2. **TestResolveList_ParseInvalid**
   - Missing parts: `example.com:443` (only 2 parts)
   - Invalid port: `example.com:abc:192.168.1.1`
   - Invalid IP: `example.com:443:not-an-ip`
   - Empty fields: `example.com::192.168.1.1`

3. **TestResolveList_Multiple**
   - Add multiple overrides
   - Verify map contains all entries
   - Test overriding same host:port (last wins)

**Acceptance Criteria**:
- All tests pass with `-race` flag
- Tests use local echo server (existing pattern)
- Tests verify actual connection behavior, not just parsing
- Coverage for both override and fallback paths

---

### Step 7: Add Integration Test
**File**: `internal/app/client_test.go` or new `integration_test.go`

**Test Case**: End-to-end CLI flow simulation
```go
func TestClient_MeasureLatency_WithResolve(t *testing.T) {
    // Start local echo server on :8080
    server := startEchoServer(t)
    defer server.Close()

    // Create client with resolve override pointing example.com:443 → 127.0.0.1:8080
    client := NewClient(
        WithResolves(map[string]string{
            "example.com:443": "127.0.0.1",
        }),
        WithTextMessage("test"),
    )

    // Parse URL with overridden host
    targetURL, _ := url.Parse("ws://example.com:8080/")

    result, err := client.MeasureLatency(context.Background(), targetURL)
    require.NoError(t, err)
    require.NotNil(t, result)

    // Verify connection succeeded (proves override worked)
    assert.Greater(t, result.MessageCount, 0)
}
```

**Acceptance Criteria**:
- Test simulates real CLI usage
- Verifies DNS override actually changes connection target
- Tests with local server (no external dependencies)

---

### Step 8: Update Documentation
**Files**: `README.md`, `cmd/wsstat/main.go` (usage text)

**Tasks**:
1. **In `printUsage()`** (already covered in Step 2):
   - Add under "Connection:" section:
     ```
     --resolve <string>         resolve host:port to specific address
                                (format: HOST:PORT:ADDRESS; repeatable)
     ```

2. **In `README.md`**:
   - Add to "Connection Options" section
   - Add example under "Advanced Usage":
     ```markdown
     ## Advanced Usage

     ### Custom DNS Resolution

     Override DNS resolution for specific host:port combinations:

     ```bash
     # Test production endpoint against staging IP
     wsstat --resolve api.example.com:443:192.168.1.50 wss://api.example.com/ws

     # Test with localhost
     wsstat --resolve echo.example.com:443:127.0.0.1 wss://echo.example.com/ws
     ```
     ```

3. **Add code comments**:
   - Document `resolveList` type with examples
   - Document `WithResolves()` option with format specification

**Acceptance Criteria**:
- Help text shows `--resolve` option
- README includes practical examples
- Format specification is clear and unambiguous

---

### Step 9: Manual Testing Checklist
**Before PR**:

1. **Basic override**:
   ```bash
   # Start local echo server
   go run ./testdata/echo-server.go  # or equivalent

   # Test override
   wsstat --resolve localhost:443:127.0.0.1 ws://localhost:8080
   ```

2. **Multiple overrides**:
   ```bash
   wsstat --resolve host1:443:192.168.1.1 \
          --resolve host2:443:192.168.1.2 \
          wss://host1/ws
   ```

3. **Invalid inputs**:
   ```bash
   # Should fail with clear error
   wsstat --resolve invalid-format wss://example.com
   wsstat --resolve example.com:99999:127.0.0.1 wss://example.com
   ```

4. **Verify no DNS leak**:
   - Override with non-existent hostname
   - Verify connection attempts to override IP (not DNS failure)

5. **IPv6 support**:
   ```bash
   wsstat --resolve localhost:443:::1 ws://localhost:8080
   ```

**Acceptance Criteria**:
- All manual tests pass
- Error messages are helpful
- No DNS lookups when override is active

---

### Step 10: Code Quality and PR Preparation
**Tasks**:

1. **Run linters**:
   ```bash
   make lint
   ```

2. **Run all tests**:
   ```bash
   make test RACE=1 V=1
   ```

3. **Format code**:
   ```bash
   make fmt
   ```

4. **Verify imports**:
   - Grouped: stdlib, third-party, local
   - Alphabetical within groups
   - Blank lines between groups

5. **Review checklist**:
   - [ ] All new code has tests
   - [ ] Tests pass with `-race`
   - [ ] No backwards-incompatible changes
   - [ ] Documentation updated
   - [ ] Error messages are actionable
   - [ ] Code follows existing patterns (options, validation)
   - [ ] No leaked goroutines or resources

**Acceptance Criteria**:
- `make lint && make test` passes cleanly
- Code review ready
- No outstanding TODOs

---

## Files to Modify/Create

### New Files
- None (all changes to existing files)

### Modified Files
1. `cmd/wsstat/flags.go` - Add `resolveList` type
2. `cmd/wsstat/main.go` - Add flag registration and usage
3. `cmd/wsstat/config.go` - Add `Resolves` field
4. `internal/app/client.go` - Add `resolves` field and option
5. `wsstat.go` - Add option, modify `resolveDialTargets()`, `newDialer()`
6. `wsstat_test.go` - Add unit tests
7. `cmd/wsstat/flags_test.go` - Add flag parsing tests
8. `internal/app/client_test.go` - Add integration test (or new file)
9. `README.md` - Add documentation

---

## Edge Cases to Handle

1. **Port normalization**: Ensure default ports (80, 443) match URL parsing
   - `wss://example.com` → port 443 implicit
   - Override must be `example.com:443:...`

2. **IPv6 brackets**: Handle both `::1` and `[::1]` formats
   - Strip brackets if present in override value

3. **Case sensitivity**:
   - Hostnames are case-insensitive
   - Normalize to lowercase for map key

4. **Invalid override IP at dial time**:
   - Let dial fail naturally with connection error
   - Don't validate IP format at flag parsing (might be hostname in future)
   - Actually, DO validate IP at parse time for better UX

5. **Empty override map**:
   - No performance impact (quick map lookup returns false)
   - Original DNS behavior unchanged

---

## Future Enhancements (Out of Scope)

- Support hostname in override value (not just IP)
- Wildcard patterns: `*.example.com:443:192.168.1.1`
- Read overrides from file: `--resolve-file overrides.txt`
- Integration with system resolver plugins

---

## Success Criteria

1. ✅ CLI accepts `--resolve HOST:PORT:ADDRESS` flag
2. ✅ Multiple overrides work (flag is repeatable)
3. ✅ DNS lookup skipped when override exists
4. ✅ DNS fallback works when no override
5. ✅ IPv4 and IPv6 addresses supported
6. ✅ All tests pass with race detector
7. ✅ Documentation complete and accurate
8. ✅ No breaking changes to existing API
9. ✅ Code passes `make lint` and `make test`
10. ✅ Manual testing scenarios pass
