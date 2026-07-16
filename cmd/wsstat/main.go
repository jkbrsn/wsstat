// Package main implements the wsstat command-line tool for measuring WebSocket
// connection latency and streaming subscription events.
//
// The CLI provides a simple interface to check WebSocket endpoint status,
// measure connection timing (DNS, TCP, TLS, WebSocket handshake, and message RTT),
// and stream long-lived subscription feeds.
//
// # Basic Usage
//
//	wsstat example.org
//	wsstat -t "ping" wss://echo.example.com
//	wsstat --rpc-method eth_blockNumber wss://rpc.example.com/ws
//
// # Stream Mode
//
// For long-lived streaming endpoints, use the stream subcommand to keep the
// connection open and forward incoming frames to stdout:
//
//	wsstat stream -t '{"method":"subscribe"}' wss://stream.example.com
//	wsstat stream --once -t '{"method":"ticker"}' wss://api.example.com
//
// # Ping Mode
//
// To watch ping/pong latency over time, use the ping subcommand, which sends a WebSocket
// ping frame every interval on a single connection and prints per-ping RTT plus a summary:
//
//	wsstat ping -c 5 wss://echo.example.com
//	wsstat ping -i 500ms -w 30s wss://echo.example.com
//
// # Architecture
//
// The package is organized into:
//   - main.go: Entry point, subcommand dispatch, run paths, and usage text
//   - config.go: Shared flag registration, validation, and URL handling
//   - flags.go: Custom flag.Value implementations for headers and resolve overrides
//
// All business logic is delegated to the internal/app package, keeping cmd/wsstat
// focused on CLI concerns (parsing, validation, help text, and error formatting).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jkbrsn/wsstat/v3/internal/app"
)

var version = "unknown"

// Process exit codes. Usage covers both flag-parse failures and post-parse argument
// validation; runtime is reserved for genuine connection/measurement/output failures.
const (
	exitRuntime     = 1 // runtime/network failure (dial, measure, stream, output write)
	exitUsage       = 2 // bad invocation: flag parse error or argument validation
	exitCheckFailed = 3 // check mode: one or more conformance checks failed
)

// errUsageShown signals that a FlagSet already printed its own error and usage,
// so main should exit without printing anything further.
var errUsageShown = errors.New("usage shown")

// errVersionShown signals that --version was handled on a subcommand; main
// exits 0 with no further output.
var errVersionShown = errors.New("version shown")

// errPingTotalLoss signals ping-mode total loss (zero pongs received). RunPing has already
// printed the per-ping lines and the summary, so main exits with the runtime code without
// printing an additional error line or JSON envelope, keeping ping_summary the final record.
// A dial failure (nothing printed yet) still flows through runtimeErr instead.
var errPingTotalLoss = errors.New("no pongs received")

// errCheckFailed signals check-mode conformance failure (one or more checks are `fail`).
// RunCheck/PrintCheckResults have already emitted the report, so main exits with
// exitCheckFailed without printing an additional error line or JSON envelope. A failed check
// is not a runtime error: dial and output-write failures still flow through runtimeErr.
var errCheckFailed = errors.New("one or more checks failed")

// responseFilePerm is the mode for the --file response sink (owner read/write, group/other read).
const responseFilePerm = 0o644

// versionFmt is the format string for the "wsstat <version>" line printed on --version.
const versionFmt = "wsstat %s\n"

// cliError classifies a failure for the top-level handler: the process exit code and
// the resolved output contract, so a JSON run can emit a structured error envelope.
type cliError struct {
	code   int        // process exit code (exitRuntime or exitUsage)
	output app.Output // resolved output contract; OutputJSON triggers the JSON envelope
	err    error      // underlying error
}

func (e *cliError) Error() string { return e.err.Error() }
func (e *cliError) Unwrap() error { return e.err }

// usageErr classifies a build/validation failure as exit 2. The flag-parse sentinels
// (already-printed usage, help) pass through unchanged for main's dispatch switch.
func usageErr(err error) error {
	if errors.Is(err, flag.ErrHelp) || errors.Is(err, errUsageShown) ||
		errors.Is(err, errVersionShown) {
		return err
	}
	return &cliError{code: exitUsage, err: err}
}

// runtimeErr classifies a runtime failure as exit 1, carrying the output contract so
// fail can emit a JSON error envelope under -o json. Returns nil for a nil error.
func runtimeErr(output app.Output, err error) error {
	if err == nil {
		return nil
	}
	return &cliError{code: exitRuntime, output: output, err: err}
}

// removedFlags maps v2 flags dropped in v3 to a targeted migration hint.
var removedFlags = map[string]string{
	"subscribe":      "use the `stream` subcommand: wsstat stream <url>",
	"s":              "use the `stream` subcommand: wsstat stream <url>",
	"subscribe-once": "use `stream --once`: wsstat stream --once <url>",
	"format":         "use -o (text|json|raw), --body, and/or --clip",
	"no-tls":         "type a ws:// URL instead",
}

func main() {
	args := os.Args[1:]

	// Dispatch keys on args[0] only. Scanning past leading flags for a subcommand
	// is unsafe with stdlib flag: it can't know which -x tokens consume a following
	// value, so a flag value could be mistaken for a command (e.g. `wsstat -t stream
	// <url>` would misread the text message "stream" as the stream subcommand).
	// Consequence: global flags cannot precede the subcommand (the go test rule).
	var err error
	switch {
	case len(args) == 0:
		printTopUsage(os.Stderr)
		os.Exit(exitUsage)
	case args[0] == "stream":
		err = runStream(args[1:])
	case args[0] == "measure":
		err = runMeasure(args[1:])
	case args[0] == "ping":
		err = runPing(args[1:])
	case args[0] == "check":
		err = runCheck(args[1:])
	case args[0] == "--version" || args[0] == "-version":
		fmt.Printf("wsstat %s\n", version)
		return
	case isHelpArg(args[0]):
		printHelpFor(args[1:], os.Stdout)
		return
	default:
		err = runMeasure(args) // bare form: measure
	}

	switch {
	case err == nil, errors.Is(err, flag.ErrHelp), errors.Is(err, errVersionShown):
		return
	case errors.Is(err, errUsageShown):
		os.Exit(exitUsage)
	case errors.Is(err, errPingTotalLoss):
		// The summary already reported the loss; exit non-zero without extra output.
		os.Exit(exitRuntime)
	case errors.Is(err, errCheckFailed):
		// The report already showed the failing checks; exit 3 without extra output.
		os.Exit(exitCheckFailed)
	default:
		fail(err)
	}
}

// isHelpArg reports whether a top-level argument requests help. It accepts every
// spelling stdlib flag treats as help (`-h`, `-help`, `--help`) plus the bare
// `help` subcommand word, so `wsstat -help` reaches printTopUsage instead of
// falling through to measure.
func isHelpArg(arg string) bool {
	switch arg {
	case "help", "-h", "-help", "--help":
		return true
	default:
		return false
	}
}

// fail reports err and exits. A runtime failure under -o json emits a structured error
// envelope to stdout (keeping the JSON stream parseable); every other case prints plain
// text to stderr. The exit code comes from the cliError classification, defaulting to
// exitRuntime for any unclassified error.
func fail(err error) {
	code := exitRuntime
	var ce *cliError
	if errors.As(err, &ce) {
		code = ce.code
		if ce.output == app.OutputJSON {
			if emitErr := app.EmitJSONError(os.Stdout, err); emitErr == nil {
				// revive:disable-next-line:deep-exit single CLI error exit point
				os.Exit(code)
			}
			// Fall through to stderr if the envelope could not be written.
		}
	}
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	// revive:disable-next-line:deep-exit single CLI error exit point
	os.Exit(code)
}

// registerRemoved registers v2 flags dropped in v3 as inert vars on fs, matching
// their v2 arity (bool vs value-taking) so the parser consumes any value and never
// misreads a following argument (e.g. the "-s" in `-t -s` stays the text payload).
// Whether one was actually used is reported by removedFlagError after Parse.
func registerRemoved(fs *flag.FlagSet) {
	for _, name := range []string{"subscribe", "s", "subscribe-once", "no-tls"} {
		fs.Bool(name, false, "removed in v3")
	}
	fs.String("format", "", "removed in v3")
}

// removedFlagError returns a targeted migration error if any flag removed in v3
// was explicitly set on fs. Detection runs after Parse, so it sees only genuine
// flag tokens, never values that merely look like a removed flag.
func removedFlagError(fs *flag.FlagSet) error {
	var err error
	fs.Visit(func(f *flag.Flag) {
		if err != nil {
			return
		}
		if hint, ok := removedFlags[f.Name]; ok {
			err = fmt.Errorf("-%s was removed in v3; %s", f.Name, hint)
		}
	})
	return err
}

// interruptContext returns a context canceled on the first SIGINT/SIGTERM, beginning a
// graceful shutdown (which bounds the close handshake via close-grace). A second signal
// hard-exits immediately, so a teardown stuck on a non-echoing peer can always be escaped.
// Exit code 130 = 128 + SIGINT.
func interruptContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
			return
		}
		<-sigCh
		// revive:disable-next-line:deep-exit second signal force-quits a stuck teardown
		os.Exit(130)
	}()
	return ctx, cancel
}

// buildMeasure parses measure-mode args and returns a validated client and target.
func buildMeasure(args []string) (*app.Client, *url.URL, error) {
	fs := flag.NewFlagSet("measure", flag.ContinueOnError)
	cf := newCommonFlags()
	registerCommon(fs, &cf)
	registerRemoved(fs)
	count := fs.Int("c", 1, "number of interactions to perform (>= 1)")
	fs.IntVar(count, "count", 1, "number of interactions to perform (>= 1)")
	fs.Usage = func() {} // parseErr owns usage printing (stdout for -h, stderr otherwise)

	if err := fs.Parse(args); err != nil {
		return nil, nil, parseErr(err, printMeasureUsage)
	}
	if cf.version {
		fmt.Printf("wsstat %s\n", version)
		return nil, nil, errVersionShown
	}
	if err := removedFlagError(fs); err != nil {
		return nil, nil, err
	}

	opts, target, err := resolveCommon(fs, &cf, app.ModeMeasure)
	if err != nil {
		return nil, nil, err
	}
	if *count < 1 {
		return nil, nil, errors.New("count must be greater than 0")
	}
	opts = append(opts, app.WithCount(*count))

	client := app.NewClient(opts...)
	if err := client.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid settings: %w", err)
	}
	return client, target, nil
}

// buildStream parses stream-mode args and returns a validated client and target.
// Whether --once was requested is available via client.Once().
func buildStream(args []string) (*app.Client, *url.URL, error) {
	fs := flag.NewFlagSet("stream", flag.ContinueOnError)
	cf := newCommonFlags()
	registerCommon(fs, &cf)
	registerRemoved(fs)
	count := fs.Int("c", 0, "number of events to receive; 0 = unlimited")
	fs.IntVar(count, "count", 0, "number of events to receive; 0 = unlimited")
	once := fs.Bool("once", false, "exit after the first event")
	sendDelay := fs.Duration("send-delay", time.Second,
		"delay between successive -t sends (with repeated -t)")
	buffer := fs.Int("b", 0, "delivery buffer size (messages)")
	fs.IntVar(buffer, "buffer", 0, "delivery buffer size (messages)")
	summary := fs.Duration("summary-interval", 0,
		"print stat summaries every interval (e.g., 5s, 1m); 0 disables")
	fs.Usage = func() {} // parseErr owns usage printing (stdout for -h, stderr otherwise)

	if err := fs.Parse(args); err != nil {
		return nil, nil, parseErr(err, printStreamUsage)
	}
	if cf.version {
		fmt.Printf("wsstat %s\n", version)
		return nil, nil, errVersionShown
	}
	if err := removedFlagError(fs); err != nil {
		return nil, nil, err
	}

	opts, target, err := resolveCommon(fs, &cf, app.ModeStream)
	if err != nil {
		return nil, nil, err
	}
	set := setFlagNames(fs)
	if *count < 0 {
		return nil, nil, errors.New("count must be zero or greater")
	}
	if *once {
		if set["c"] || set["count"] {
			return nil, nil, errors.New("--count cannot be combined with --once")
		}
	}
	if *summary > 0 {
		if out, _ := app.ParseOutput(cf.output); out == app.OutputRaw {
			return nil, nil, errors.New("--summary-interval has no effect with -o raw")
		}
	}
	if *sendDelay < 0 {
		return nil, nil, errors.New("--send-delay must be zero or greater")
	}
	if set["send-delay"] && len(cf.text) < 2 {
		return nil, nil, errors.New("--send-delay has no effect without repeated -t")
	}
	opts = append(opts,
		app.WithCount(*count),
		app.WithStreamOnce(*once),
		app.WithBuffer(*buffer),
		app.WithSummaryInterval(*summary),
		app.WithSendDelay(*sendDelay),
	)

	client := app.NewClient(opts...)
	if err := client.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid settings: %w", err)
	}
	return client, target, nil
}

// buildCheck parses check-mode args and returns a validated client and target. Check mode adds
// no subcommand-specific flags in v1; it dials its own fixed catalog of connections.
func buildCheck(args []string) (*app.Client, *url.URL, error) {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	cf := newCommonFlags()
	registerCommon(fs, &cf)
	registerRemoved(fs)
	fs.Usage = func() {} // parseErr owns usage printing (stdout for -h, stderr otherwise)

	if err := fs.Parse(args); err != nil {
		return nil, nil, parseErr(err, printCheckUsage)
	}
	if cf.version {
		fmt.Printf(versionFmt, version)
		return nil, nil, errVersionShown
	}
	if err := removedFlagError(fs); err != nil {
		return nil, nil, err
	}

	opts, target, err := resolveCommon(fs, &cf, app.ModeCheck)
	if err != nil {
		return nil, nil, err
	}

	client := app.NewClient(opts...)
	if err := client.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid settings: %w", err)
	}
	return client, target, nil
}

// unsupportedFlag describes a flag a subcommand rejects: the reason shown in the error,
// and whether the flag takes a value (its arity), so the stub registration consumes any
// value token: -t @- must not read stdin, and a following value must not be misread as
// the positional URL.
type unsupportedFlag struct {
	reason string
	valued bool
}

// pingUnsupported maps the flags the ping subcommand rejects (internal flag name,
// without dashes) to their rejection details.
var pingUnsupported = map[string]unsupportedFlag{
	"t":           {"ping sends ping frames, not messages", true},
	"text":        {"ping sends ping frames, not messages", true},
	"rpc-method":  {"ping sends ping frames, not messages", true},
	"rpc-version": {"ping sends ping frames, not messages", true},
	"f":           {"ping has no response payloads to record", true},
	"file":        {"ping has no response payloads to record", true},
	"body":        {"ping renders no response bodies", true},
	"clip":        {"ping renders no response bodies", false},
}

// registerUnsupported registers a subcommand's unsupported flags as inert stubs on fs,
// matching each flag's arity. Whether one was actually used is reported by
// unsupportedFlagError after Parse, keyed on the flag being set rather than its resolved
// value, so an explicitly empty value (-t ”, --file ”) is rejected too. Mirrors the
// app-layer validatePing rejections for the direct-API path.
func registerUnsupported(fs *flag.FlagSet, flags map[string]unsupportedFlag) {
	for name, f := range flags {
		if f.valued {
			fs.String(name, "", "not supported by this subcommand")
		} else {
			fs.Bool(name, false, "not supported by this subcommand")
		}
	}
}

// unsupportedFlagError returns a targeted error if any of the subcommand's unsupported
// flags was explicitly set on fs. Detection runs after Parse, so it sees only genuine
// flag tokens, never values that merely look like one.
func unsupportedFlagError(fs *flag.FlagSet, cmd string, flags map[string]unsupportedFlag) error {
	var err error
	fs.Visit(func(f *flag.Flag) {
		if err != nil {
			return
		}
		if u, ok := flags[f.Name]; ok {
			dashes := "--"
			if len(f.Name) == 1 {
				dashes = "-"
			}
			err = fmt.Errorf("%s%s is not supported in %s mode: %s", dashes, f.Name, cmd, u.reason)
		}
	})
	return err
}

// pingConfig bundles the validated ping-mode settings. The deadline is CLI-layer-only (it
// never reaches app.Client), so it rides alongside the client rather than inside it.
type pingConfig struct {
	client   *app.Client
	target   *url.URL
	deadline time.Duration
}

// buildPing parses ping-mode args and returns the validated ping configuration.
func buildPing(args []string) (pingConfig, error) {
	fs := flag.NewFlagSet("ping", flag.ContinueOnError)
	cf := newCommonFlags()
	registerOutputFlags(fs, &cf)
	registerConnectionFlags(fs, &cf)
	registerDiagnosticFlags(fs, &cf)
	registerUnsupported(fs, pingUnsupported)
	registerRemoved(fs)
	count := fs.Int("c", 0, "number of pings to send; 0 = until interrupted")
	fs.IntVar(count, "count", 0, "number of pings to send; 0 = until interrupted")
	interval := fs.Duration("i", time.Second, "delay between pings (e.g., 500ms, 2s)")
	fs.DurationVar(interval, "interval", time.Second, "delay between pings (e.g., 500ms, 2s)")
	deadline := fs.Duration("w", 0, "max total run time (e.g., 10s); 0 = no deadline")
	fs.DurationVar(deadline, "deadline", 0, "max total run time (e.g., 10s); 0 = no deadline")
	fs.Usage = func() {} // parseErr owns usage printing (stdout for -h, stderr otherwise)

	if err := fs.Parse(args); err != nil {
		return pingConfig{}, parseErr(err, printPingUsage)
	}
	if cf.version {
		fmt.Printf("wsstat %s\n", version)
		return pingConfig{}, errVersionShown
	}
	if err := removedFlagError(fs); err != nil {
		return pingConfig{}, err
	}

	if err := unsupportedFlagError(fs, "ping", pingUnsupported); err != nil {
		return pingConfig{}, err
	}

	set := setFlagNames(fs)
	opts, target, err := resolveCommon(fs, &cf, app.ModePing)
	if err != nil {
		return pingConfig{}, err
	}
	if *count < 0 {
		return pingConfig{}, errors.New("count must be zero or greater")
	}
	// --deadline is validated here (not in Validate) since it never reaches app.Client.
	if (set["w"] || set["deadline"]) && *deadline <= 0 {
		return pingConfig{}, errors.New("--deadline must be greater than zero")
	}
	opts = append(opts, app.WithCount(*count), app.WithInterval(*interval))

	client := app.NewClient(opts...)
	if err := client.Validate(); err != nil {
		return pingConfig{}, fmt.Errorf("invalid settings: %w", err)
	}
	return pingConfig{client: client, target: target, deadline: *deadline}, nil
}

// parseErr maps a FlagSet parse error to the appropriate sentinel and prints the
// command usage: to stdout for explicitly requested help (GNU convention, and
// matching top-level -h), to stderr after a genuine parse error (whose message
// the FlagSet already printed there).
func parseErr(err error, usage func(io.Writer)) error {
	if errors.Is(err, flag.ErrHelp) {
		usage(os.Stdout)
		return flag.ErrHelp
	}
	usage(os.Stderr)
	return errUsageShown
}

// countingWriter wraps a buffered writer and tracks the byte count, so the response-sink
// closer can tell whether anything was ever recorded.
type countingWriter struct {
	w *bufio.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// openResponseSink opens the --file response sink (if configured) and injects it into the
// client, returning a closer to defer. The file is opened O_EXCL so an existing capture is
// never clobbered, and writes are buffered for throughput on high-frequency streams. The
// closer flushes and closes the file, then removes it if nothing was recorded so a failed
// or payload-less run leaves no empty file to block the next attempt. Returns a no-op closer
// when --file is unset.
func openResponseSink(client *app.Client, out app.Output) (func(), error) {
	path := client.ResponseFilePath()
	if path == "" {
		return func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, responseFilePerm)
	if err != nil {
		return nil, runtimeErr(out, fmt.Errorf("opening response file: %w", err))
	}
	cw := &countingWriter{w: bufio.NewWriter(f)}
	client.SetResponseSink(cw)
	return func() {
		_ = cw.w.Flush()
		_ = f.Close()
		if cw.n == 0 {
			_ = os.Remove(path)
		}
	}, nil
}

func runMeasure(args []string) error {
	client, target, err := buildMeasure(args)
	if err != nil {
		return usageErr(err)
	}

	ctx, cancel := interruptContext()
	defer cancel()

	out := client.Output()
	closeSink, err := openResponseSink(client, out)
	if err != nil {
		return err
	}
	defer closeSink()

	result, err := client.MeasureLatency(ctx, target)
	if err != nil {
		return runtimeErr(out, fmt.Errorf("measuring latency: %w", err))
	}

	if err := client.PrintRequestDetails(result); err != nil {
		return runtimeErr(out, fmt.Errorf("printing request details: %w", err))
	}
	if err := client.PrintTimingResults(target, result); err != nil {
		return runtimeErr(out, fmt.Errorf("printing timing results: %w", err))
	}
	// Record before printing so the durable side-channel capture is independent of stdout:
	// a formatting failure on the print path must not skip the recording.
	if err := client.RecordResponse(result); err != nil {
		return runtimeErr(out, fmt.Errorf("recording response: %w", err))
	}
	if err := client.PrintResponse(result); err != nil {
		return runtimeErr(out, fmt.Errorf("printing response: %w", err))
	}
	return nil
}

func runStream(args []string) error {
	client, target, err := buildStream(args)
	if err != nil {
		return usageErr(err)
	}

	ctx, cancel := interruptContext()
	defer cancel()

	out := client.Output()
	closeSink, err := openResponseSink(client, out)
	if err != nil {
		return err
	}
	defer closeSink()

	if client.Once() {
		return runtimeErr(out, client.StreamSubscriptionOnce(ctx, target))
	}
	return runtimeErr(out, client.StreamSubscription(ctx, target))
}

// runPing runs ping mode. Unlike runMeasure/runStream it does not funnel every returned error
// through runtimeErr: RunPing swallows context cancellation (Ctrl-C, --deadline) and connection
// loss, returning the report with a nil error, so those never become exit 1. runPing derives
// the exit code from the report instead: zero pongs received (total loss) is exit 1, making
// `wsstat ping -c N <url>` a usable liveness gate; any pong received is exit 0. Only dial and
// output-write failures flow through the error return.
func runPing(args []string) error {
	cfg, err := buildPing(args)
	if err != nil {
		return usageErr(err)
	}

	ctx, cancel := interruptContext()
	defer cancel()
	if cfg.deadline > 0 {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithTimeout(ctx, cfg.deadline)
		defer deadlineCancel()
	}

	out := cfg.client.Output()
	report, err := cfg.client.RunPing(ctx, cfg.target)
	if err != nil {
		return runtimeErr(out, err)
	}
	if report.Received == 0 {
		return errPingTotalLoss
	}
	return nil
}

// runCheck runs check mode. It prints the report on every non-runtime path and derives the exit
// code from the verdicts: any `fail` yields exitCheckFailed (via errCheckFailed), making
// `wsstat check <url>` a CI gate; warnings alone exit 0. Only dial and output-write failures
// flow through runtimeErr (exit 1); under -o json those emit a structured error envelope.
func runCheck(args []string) error {
	client, target, err := buildCheck(args)
	if err != nil {
		return usageErr(err)
	}

	ctx, cancel := interruptContext()
	defer cancel()

	out := client.Output()
	report, err := client.RunCheck(ctx, target)
	if err != nil {
		return runtimeErr(out, fmt.Errorf("running checks: %w", err))
	}
	if err := client.PrintCheckResults(report); err != nil {
		return runtimeErr(out, fmt.Errorf("printing check results: %w", err))
	}
	if report.Failed() > 0 {
		return errCheckFailed
	}
	return nil
}
