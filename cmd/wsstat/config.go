package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jkbrsn/wsstat/v3/internal/app"
)

const (
	decimalBase  = 10      // base for parsing decimal integer flag values
	int64BitSize = 64      // bit size for strconv.ParseInt into int64
	bytesPerKiB  = 1 << 10 // multiplier for the K suffix on --max-message-size
	bytesPerMiB  = 1 << 20 // multiplier for the M suffix on --max-message-size
	// maxCloseGrace is the transport's hard-coded close-handshake cap; --close-timeout
	// values above it have no effect (coder/websocket bounds the echo wait at 5s).
	maxCloseGrace = 5 * time.Second
)

// revive:disable:line-length-limit flag descriptions

// commonFlags holds the connection, input, and output flags shared by every
// subcommand. Mode-specific flags (count, --once, --buffer, --summary-interval)
// are registered separately by each command.
type commonFlags struct {
	headers     headerList
	resolves    resolveList
	rpcMethod   string
	rpcVersion  string
	text        string
	output      string
	body        string
	color       string
	clip        bool
	showSecrets bool
	quiet       bool
	v1          bool
	v2          bool
	insecure    bool

	timeout      time.Duration
	closeTimeout time.Duration

	maxMessageSize string
	subprotocol    string
	validateUTF8   bool

	debug bool
}

// registerCommon registers the shared flags onto fs, binding them to c.
func registerCommon(fs *flag.FlagSet, c *commonFlags) {
	fs.Var(&c.headers, "H", "HTTP header to include with the request (repeatable; format: \"Key: Value\")")
	fs.Var(&c.headers, "header", "HTTP header to include with the request (repeatable; format: \"Key: Value\")")
	fs.Var(&c.resolves, "resolve", "resolve host:port to address (repeatable; format: \"HOST:PORT:ADDRESS\")")

	fs.StringVar(&c.rpcMethod, "rpc-method", "", "JSON-RPC method name to send (id=1, jsonrpc=2.0)")
	fs.StringVar(&c.rpcVersion, "rpc-version", "2.0", "JSON-RPC version for --rpc-method: 2.0 or 1.0")
	fs.StringVar(&c.text, "t", "", "text message to send (@file or @- reads payload from a file or stdin)")
	fs.StringVar(&c.text, "text", "", "text message to send (@file or @- reads payload from a file or stdin)")

	fs.StringVar(&c.output, "o", "text", "output contract: text, json, or raw")
	fs.StringVar(&c.output, "output", "text", "output contract: text, json, or raw")
	fs.StringVar(&c.body, "body", "auto", "body rendering for text output: auto or compact")
	fs.BoolVar(&c.clip, "clip", false, "clip each rendered line to terminal width (text output, TTY only)")
	fs.BoolVar(&c.showSecrets, "show-secrets", false,
		"show sensitive header values in -vv output instead of masking them")
	fs.BoolVar(&c.quiet, "q", false, "suppress all output except the response")
	fs.BoolVar(&c.quiet, "quiet", false, "suppress all output except the response")
	fs.BoolVar(&c.v1, "v", false, "increase verbosity (level 1)")
	fs.BoolVar(&c.v1, "verbose", false, "increase verbosity (level 1)")
	fs.BoolVar(&c.v2, "vv", false, "increase verbosity (level 2)")

	fs.BoolVar(&c.insecure, "k", false, "skip TLS certificate verification")
	fs.BoolVar(&c.insecure, "insecure", false, "skip TLS certificate verification")
	fs.StringVar(&c.color, "color", "auto", "color output: auto, always, or never")
	fs.DurationVar(&c.timeout, "timeout", 0, "read/dial timeout (e.g., 30s, 1m); 0 uses default (5s)")
	fs.DurationVar(&c.closeTimeout, "close-timeout", 0,
		"max wait for the peer's close echo before forcing teardown; 0 uses default (3s); capped at 5s")
	fs.StringVar(&c.maxMessageSize, "max-message-size", "",
		"max inbound message size, e.g. 512K or 16M; empty uses default (16M); -1 disables the limit")
	fs.StringVar(&c.subprotocol, "subprotocol", "",
		"WebSocket subprotocol(s) to negotiate, in preference order (comma-separated)")
	fs.BoolVar(&c.validateUTF8, "validate-utf8", false,
		"validate UTF-8 on inbound text frames and warn on violations (coder/websocket skips this)")
	fs.BoolVar(&c.debug, "debug", false,
		"emit core debug logs to stderr (independent of -v/-vv output verbosity)")
}

// textOnlyFlags maps the internal flag names rejected under json/raw output to
// their canonical CLI spelling for error messages.
var textOnlyFlags = []struct{ name, display string }{
	{"body", "--body"},
	{"clip", "--clip"},
	{"show-secrets", "--show-secrets"},
	{"q", "-q"},
	{"quiet", "--quiet"},
	{"v", "-v"},
	{"verbose", "--verbose"},
	{"vv", "-vv"},
}

// resolveCommon validates the shared flags and returns the common client options
// plus the parsed target URL. mode is needed for output-axis validation.
func resolveCommon(fs *flag.FlagSet, c *commonFlags, mode app.Mode) ([]app.Option, *url.URL, error) {
	set := setFlagNames(fs)

	output, err := app.ParseOutput(c.output)
	if err != nil {
		return nil, nil, err
	}

	body, err := app.ParseBody(c.body)
	if err != nil {
		return nil, nil, err
	}

	color := strings.ToLower(strings.TrimSpace(c.color))
	switch color {
	case "auto", "always", "never":
	default:
		return nil, nil, errors.New("-color must be auto, always, or never")
	}

	verbosity := 0
	if c.v2 {
		verbosity = 2
	} else if c.v1 {
		verbosity = 1
	}
	if c.quiet && verbosity > 0 {
		return nil, nil, errors.New("-q cannot be combined with -v or -vv")
	}

	if err := resolveTextPayload(c); err != nil {
		return nil, nil, err
	}

	rpcVersion, err := validateMessaging(c, set)
	if err != nil {
		return nil, nil, err
	}

	if c.timeout < 0 {
		return nil, nil, errors.New("--timeout must be zero or greater")
	}
	if c.closeTimeout < 0 {
		return nil, nil, errors.New("--close-timeout must be zero or greater")
	}
	if c.closeTimeout > maxCloseGrace {
		fmt.Fprintf(os.Stderr,
			"notice: --close-timeout %s has no effect above 5s; "+
				"the transport caps the close handshake at 5s\n", c.closeTimeout)
	}

	readLimit, err := parseReadLimit(c.maxMessageSize)
	if err != nil {
		return nil, nil, err
	}

	// Axis purity: --body/--clip/-q/-v/-vv are text-only; reject under json/raw.
	if err := validateTextOnlyFlags(output, set); err != nil {
		return nil, nil, err
	}

	// Raw measure has no payload to emit without a message.
	if output == app.OutputRaw && mode == app.ModeMeasure && c.text == "" && c.rpcMethod == "" {
		return nil, nil, errors.New("-o raw in measure mode requires --text or --rpc-method")
	}

	target, err := positionalURL(fs)
	if err != nil {
		return nil, nil, err
	}

	opts := buildCommonOptions(c, mode, output, body, color, verbosity, rpcVersion, readLimit)
	return opts, target, nil
}

// buildCommonOptions assembles the app options from the validated common flags and derived values.
func buildCommonOptions(
	c *commonFlags, mode app.Mode, output app.Output, body app.Body,
	color string, verbosity int, rpcVersion string, readLimit int64,
) []app.Option {
	return []app.Option{
		app.WithHeaders(c.headers.Values()),
		app.WithResolves(c.resolves.Values()),
		app.WithRPCMethod(c.rpcMethod),
		app.WithRPCVersion(rpcVersion),
		app.WithTextMessage(c.text),
		app.WithOutput(output),
		app.WithBodyRender(body),
		app.WithClip(c.clip),
		app.WithShowSecrets(c.showSecrets),
		app.WithColorMode(color),
		app.WithQuiet(c.quiet),
		app.WithVerbosity(verbosity),
		app.WithInsecure(c.insecure),
		app.WithTimeout(c.timeout),
		app.WithCloseGrace(c.closeTimeout),
		app.WithReadLimit(readLimit),
		app.WithSubprotocols(splitCSV(c.subprotocol)),
		app.WithValidateUTF8(c.validateUTF8),
		app.WithDebug(c.debug),
		app.WithMode(mode),
	}
}

// resolveTextPayload expands an @-prefixed --text value: "@-" reads stdin, "@path" reads a
// file. The bytes are sent verbatim (no trailing-newline stripping), matching the raw-output
// contract. A literal leading @ is escaped as "@@". Any other value passes through unchanged.
func resolveTextPayload(c *commonFlags) error {
	if !strings.HasPrefix(c.text, "@") {
		return nil
	}
	if strings.HasPrefix(c.text, "@@") {
		c.text = c.text[1:]
		return nil
	}
	src := c.text[1:]
	var (
		data []byte
		err  error
	)
	if src == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(src)
	}
	if err != nil {
		return fmt.Errorf("reading --text payload: %w", err)
	}
	c.text = string(data)
	return nil
}

// validateMessaging checks the messaging flags (--text / --rpc-method / --rpc-version) and
// returns the resolved JSON-RPC version.
func validateMessaging(c *commonFlags, set map[string]bool) (string, error) {
	if c.text != "" && c.rpcMethod != "" {
		return "", errors.New("mutually exclusive messaging flags: use --text or --rpc-method")
	}
	rpcVersion := strings.TrimSpace(c.rpcVersion)
	switch rpcVersion {
	case "1.0", "2.0":
	default:
		return "", errors.New("--rpc-version must be 1.0 or 2.0")
	}
	if set["rpc-version"] && c.rpcMethod == "" && c.text == "" {
		return "", errors.New("--rpc-version requires --rpc-method or --text")
	}
	return rpcVersion, nil
}

// splitCSV splits a comma-separated value into trimmed, non-empty parts. Returns nil for empty.
func splitCSV(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// parseReadLimit parses a --max-message-size value into a byte count. An empty string yields 0
// (library default); a negative value yields -1 (unlimited). A trailing K or M (case-insensitive)
// multiplies by 1024 or 1024*1024.
func parseReadLimit(s string) (int64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, nil
	}
	mult := int64(1)
	digits := trimmed
	switch trimmed[len(trimmed)-1] {
	case 'K', 'k':
		mult, digits = bytesPerKiB, trimmed[:len(trimmed)-1]
	case 'M', 'm':
		mult, digits = bytesPerMiB, trimmed[:len(trimmed)-1]
	default:
		// No unit suffix; the whole string is a byte count.
	}
	n, err := strconv.ParseInt(strings.TrimSpace(digits), decimalBase, int64BitSize)
	if err != nil {
		return 0, fmt.Errorf("invalid --max-message-size %q: want a byte count like 512K or 16M, or -1", s)
	}
	if n < 0 {
		return -1, nil
	}
	return n * mult, nil
}

// validateTextOnlyFlags rejects --body/--clip/-q/-v/-vv when output is not text.
func validateTextOnlyFlags(output app.Output, set map[string]bool) error {
	if output == app.OutputText {
		return nil
	}
	var bad []string
	for _, f := range textOnlyFlags {
		if set[f.name] {
			bad = append(bad, f.display)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s only applies to text output (use -o text)", strings.Join(bad, ", "))
	}
	return nil
}

// setFlagNames returns the set of flag names explicitly provided on the command line.
func setFlagNames(fs *flag.FlagSet) map[string]bool {
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// positionalURL requires exactly one positional argument and parses it as a URL.
func positionalURL(fs *flag.FlagSet) (*url.URL, error) {
	rest := fs.Args()
	if len(rest) != 1 {
		return nil, errors.New("expected exactly one URL argument")
	}
	target, err := parseWSURI(rest[0])
	if err != nil {
		return nil, fmt.Errorf("error parsing input URI: %w", err)
	}
	return target, nil
}

// parseWSURI parses rawURI into a URL, defaulting a missing scheme to wss://. Only the
// ws and wss schemes are accepted; http/https (and anything else) are rejected so they are
// not silently dialed as plaintext by the lenient underlying dialer.
func parseWSURI(rawURI string) (*url.URL, error) {
	uri := rawURI
	if !strings.Contains(rawURI, "://") {
		uri = "wss://" + rawURI
	}
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "ws", "wss":
		return u, nil
	default:
		return nil, fmt.Errorf("unsupported scheme %q: use ws:// or wss://", u.Scheme)
	}
}
