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
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/jkbrsn/wsstat/v3/internal/app"
)

var version = "unknown"

// errUsageShown signals that a FlagSet already printed its own error and usage,
// so main should exit without printing anything further.
var errUsageShown = errors.New("usage shown")

// removedFlags maps v2 flags dropped in v3 to a targeted migration hint.
var removedFlags = map[string]string{
	"subscribe":      "use the `stream` subcommand: wsstat stream <url>",
	"s":              "use the `stream` subcommand: wsstat stream <url>",
	"subscribe-once": "use `stream --once`: wsstat stream --once <url>",
	"format":         "use -o (text|json|raw), --body, and/or --clip",
	"f":              "use -o (text|json|raw), --body, and/or --clip",
	"no-tls":         "type a ws:// URL instead",
}

func main() {
	args := os.Args[1:]

	var err error
	switch {
	case len(args) == 0:
		printTopUsage(os.Stderr)
		os.Exit(2)
	case args[0] == "stream":
		err = runStream(args[1:])
	case args[0] == "measure":
		err = runMeasure(args[1:])
	case args[0] == "--version" || args[0] == "-version":
		fmt.Printf("Version: %s\n", version)
		return
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		printTopUsage(os.Stdout)
		return
	default:
		err = runMeasure(args) // bare form: measure
	}

	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return
	case errors.Is(err, errUsageShown):
		os.Exit(2)
	default:
		fail(err)
	}
}

// fail prints err to stderr and exits with status 1.
func fail(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	// revive:disable-next-line:deep-exit single CLI error exit point
	os.Exit(1)
}

// registerRemoved registers v2 flags dropped in v3 as inert vars on fs, matching
// their v2 arity (bool vs value-taking) so the parser consumes any value and never
// misreads a following argument (e.g. the "-s" in `-t -s` stays the text payload).
// Whether one was actually used is reported by removedFlagError after Parse.
func registerRemoved(fs *flag.FlagSet) {
	for _, name := range []string{"subscribe", "s", "subscribe-once", "no-tls"} {
		fs.Bool(name, false, "removed in v3")
	}
	for _, name := range []string{"format", "f"} {
		fs.String(name, "", "removed in v3")
	}
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
	var cf commonFlags
	registerCommon(fs, &cf)
	registerRemoved(fs)
	count := fs.Int("c", 1, "number of interactions to perform (>= 1)")
	fs.IntVar(count, "count", 1, "number of interactions to perform (>= 1)")
	fs.Usage = func() { printMeasureUsage(fs.Output()) }

	if err := fs.Parse(args); err != nil {
		return nil, nil, parseErr(err)
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
	var cf commonFlags
	registerCommon(fs, &cf)
	registerRemoved(fs)
	count := fs.Int("c", 0, "number of events to receive; 0 = unlimited")
	fs.IntVar(count, "count", 0, "number of events to receive; 0 = unlimited")
	once := fs.Bool("once", false, "exit after the first event")
	buffer := fs.Int("b", 0, "delivery buffer size (messages)")
	fs.IntVar(buffer, "buffer", 0, "delivery buffer size (messages)")
	summary := fs.Duration("summary-interval", 0,
		"print stat summaries every interval (e.g., 5s, 1m); 0 disables")
	fs.Usage = func() { printStreamUsage(fs.Output()) }

	if err := fs.Parse(args); err != nil {
		return nil, nil, parseErr(err)
	}
	if err := removedFlagError(fs); err != nil {
		return nil, nil, err
	}

	opts, target, err := resolveCommon(fs, &cf, app.ModeStream)
	if err != nil {
		return nil, nil, err
	}
	if *count < 0 {
		return nil, nil, errors.New("count must be zero or greater")
	}
	if *once {
		set := setFlagNames(fs)
		if set["c"] || set["count"] {
			return nil, nil, errors.New("--count cannot be combined with --once")
		}
	}
	if *summary > 0 {
		if out, _ := app.ParseOutput(cf.output); out == app.OutputRaw {
			return nil, nil, errors.New("--summary-interval has no effect with -o raw")
		}
	}
	opts = append(opts,
		app.WithCount(*count),
		app.WithStreamOnce(*once),
		app.WithBuffer(*buffer),
		app.WithSummaryInterval(*summary),
	)

	client := app.NewClient(opts...)
	if err := client.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid settings: %w", err)
	}
	return client, target, nil
}

// parseErr maps a FlagSet parse error to the appropriate sentinel: ErrHelp passes
// through (help already printed), anything else became errUsageShown.
func parseErr(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return flag.ErrHelp
	}
	return errUsageShown
}

func runMeasure(args []string) error {
	client, target, err := buildMeasure(args)
	if err != nil {
		return err
	}

	ctx, cancel := interruptContext()
	defer cancel()

	result, err := client.MeasureLatency(ctx, target)
	if err != nil {
		return fmt.Errorf("measuring latency: %w", err)
	}

	if err := client.PrintRequestDetails(result); err != nil {
		return fmt.Errorf("printing request details: %w", err)
	}
	if err := client.PrintTimingResults(target, result); err != nil {
		return fmt.Errorf("printing timing results: %w", err)
	}
	if err := client.PrintResponse(result); err != nil {
		return fmt.Errorf("printing response: %w", err)
	}
	return nil
}

func runStream(args []string) error {
	client, target, err := buildStream(args)
	if err != nil {
		return err
	}

	ctx, cancel := interruptContext()
	defer cancel()

	if client.Once() {
		return client.StreamSubscriptionOnce(ctx, target)
	}
	return client.StreamSubscription(ctx, target)
}
