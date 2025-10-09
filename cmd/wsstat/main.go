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
//	wsstat -text "ping" wss://echo.example.com
//	wsstat -rpc-method eth_blockNumber wss://rpc.example.com/ws
//
// # Subscription Mode
//
// For long-lived streaming endpoints, use -subscribe to keep the connection
// open and forward incoming frames to stdout:
//
//	wsstat -subscribe -text '{"method":"subscribe"}' wss://stream.example.com
//	wsstat -subscribe-once -text '{"method":"ticker"}' wss://api.example.com
//
// # Architecture
//
// The package is organized into:
//   - main.go: Entry point, flag definitions, and usage text
//   - config.go: Configuration parsing, validation, and URL handling
//   - flags.go: Custom flag.Value implementations for headers, counts, and verbosity
//
// All business logic is delegated to the internal/app package, keeping cmd/wsstat
// focused on CLI concerns (parsing, validation, help text, and error formatting).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jkbrsn/wsstat/internal/app"
)

// revive:disable:line-length-limit allow flags

var (
	// Input
	countFlag       = newTrackedIntFlag(1)
	headerArguments headerList
	rpcMethod       = flag.String("rpc-method", "", "JSON-RPC method name to send (id=1, jsonrpc=2.0)")
	textMessage     = flag.String("text", "", "text message to send")
	subscribe       = flag.Bool("subscribe", false, "stream events until interrupted")
	subscribeOnce   = flag.Bool("subscribe-once", false, "subscribe and exit after the first event")
	bufferSize      = flag.Int("buffer", 0, "subscription delivery buffer size (messages)")
	summaryInterval = flag.Duration("summary-interval", 0, "print subscription summaries every interval (e.g., 1s, 5m, 1h); 0 disables")

	// Output
	formatOption = flag.String("format", "auto", "output format: auto, json, or raw")

	// General/meta
	showVersion = flag.Bool("version", false, "print the program version")
	version     = "unknown"

	// Connection behavior
	insecure = flag.Bool("insecure", false, "skip TLS certificate verification")
	noTLS    = flag.Bool("no-tls", false, "assume ws:// when input URL lacks scheme (default wss://)")
	colorArg = flag.String("color", "auto", "color output: auto, always, or never")

	// Verbosity
	quiet = flag.Bool("q", false, "quiet all output but the response")
	v1    = flag.Bool("v", false, "increase verbosity (level 1)")
	v2    = flag.Bool("vv", false, "increase verbosity (level 2)")
)

func init() {
	// Double registration: short and long forms point to same variable
	flag.Var(&countFlag, "count", "number of interactions to perform; 0 means unlimited when subscribing")
	flag.Var(&countFlag, "c", "number of interactions to perform; 0 means unlimited when subscribing")
	flag.Var(&headerArguments, "H", "HTTP header to include with the request (repeatable; format: Key: Value)")
	flag.Var(&headerArguments, "header", "HTTP header to include with the request (repeatable; format: Key: Value)")
	flag.StringVar(textMessage, "t", "", "text message to send")
	flag.BoolVar(subscribe, "s", false, "stream events until interrupted")
	flag.IntVar(bufferSize, "b", 0, "subscription delivery buffer size (messages)")
	flag.StringVar(formatOption, "f", "auto", "output format: auto, json, or raw")
	flag.BoolVar(quiet, "quiet", false, "quiet all output but the response")
	flag.BoolVar(v1, "verbose", false, "increase verbosity (level 1)")
	flag.BoolVar(insecure, "k", false, "skip TLS certificate verification")
	// TODO: Wire -k/--insecure through config.go to TLS config (tls.Config.InsecureSkipVerify)

	flag.Usage = printUsage
}

func main() {
	if err := run(); err != nil {
		if err == errVersionRequested {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := parseConfig()
	if err != nil {
		if err == errVersionRequested {
			return err
		}
		fmt.Fprintf(os.Stderr, "Error parsing input: %v\n\n", err)
		flag.Usage()
		return err
	}

	ws := app.NewClient(
		app.WithCount(cfg.Count),
		app.WithHeaders(cfg.Headers),
		app.WithRPCMethod(cfg.RPCMethod),
		app.WithTextMessage(cfg.TextMessage),
		app.WithFormat(cfg.Format),
		app.WithColorMode(cfg.ColorMode),
		app.WithQuiet(cfg.Quiet),
		app.WithVerbosity(cfg.Verbosity),
		app.WithSubscription(cfg.Subscribe),
		app.WithSubscriptionOnce(cfg.SubscribeOnce),
		app.WithBuffer(cfg.BufferSize),
		app.WithSummaryInterval(cfg.SummaryInterval),
	)

	if err := ws.Validate(); err != nil {
		return fmt.Errorf("invalid settings: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if cfg.SubscribeOnce {
		return ws.StreamSubscriptionOnce(ctx, cfg.TargetURL)
	}

	if cfg.Subscribe {
		return ws.StreamSubscription(ctx, cfg.TargetURL)
	}

	result, err := ws.MeasureLatency(ctx, cfg.TargetURL)
	if err != nil {
		return fmt.Errorf("measuring latency: %w", err)
	}

	if !cfg.Quiet {
		if err = ws.PrintRequestDetails(result); err != nil {
			return fmt.Errorf("printing request details: %w", err)
		}

		if err = ws.PrintTimingResults(cfg.TargetURL, result); err != nil {
			return fmt.Errorf("printing timing results: %w", err)
		}
	}

	ws.PrintResponse(result)
	return nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "wsstat %s\n", version)
	fmt.Fprintln(os.Stderr, "Measure latency on WebSocket connections")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "USAGE:")
	fmt.Fprintln(os.Stderr, "  wsstat [options] <url>")
	fmt.Fprintln(os.Stderr, "  wsstat -subscribe [options] <url>")
	fmt.Fprintln(os.Stderr)

	fmt.Fprintln(os.Stderr, "General:")
	fmt.Fprintln(os.Stderr, "  -c, --count <int>              number of interactions [default: 1; unlimited when subscribing]")
	fmt.Fprintln(os.Stderr, "      --version                  print program version and exit")
	fmt.Fprintln(os.Stderr)

	fmt.Fprintln(os.Stderr, "Input (choose one):")
	fmt.Fprintln(os.Stderr, "      --rpc-method <string>      JSON-RPC method name to send (with id=1, jsonrpc=2.0)")
	fmt.Fprintln(os.Stderr, "  -t, --text <string>            text message to send")
	fmt.Fprintln(os.Stderr)

	fmt.Fprintln(os.Stderr, "Subscription:")
	fmt.Fprintln(os.Stderr, "  -s, --subscribe                stream events until interrupted")
	fmt.Fprintln(os.Stderr, "      --subscribe-once           subscribe and exit after the first event")
	fmt.Fprintln(os.Stderr, "  -b, --buffer <int>             subscription delivery buffer size in messages [default: 0]")
	fmt.Fprintln(os.Stderr, "      --summary-interval <duration>")
	fmt.Fprintln(os.Stderr, "                                 print stat summaries every interval (e.g., 5s, 1m) [default: disabled]")
	fmt.Fprintln(os.Stderr)

	fmt.Fprintln(os.Stderr, "Connection:")
	fmt.Fprintln(os.Stderr, "  -H, --header <string>          HTTP header to include with request (repeatable; format: \"Key: Value\")")
	fmt.Fprintln(os.Stderr, "  -k, --insecure                 skip TLS certificate verification (use with caution)")
	fmt.Fprintln(os.Stderr, "      --no-tls                   assume ws:// when URL lacks scheme [default: wss://]")
	fmt.Fprintln(os.Stderr, "      --color <string>           color output mode: auto, always, never [default: auto]")
	fmt.Fprintln(os.Stderr)

	fmt.Fprintln(os.Stderr, "Output:")
	fmt.Fprintln(os.Stderr, "  -q, --quiet                    suppress all output except response")
	fmt.Fprintln(os.Stderr, "  -v, --verbose                  increase verbosity (level 1)")
	fmt.Fprintln(os.Stderr, "  -vv                            increase verbosity (level 2)")
	fmt.Fprintln(os.Stderr, "  -f, --format <string>          output format: auto, json, raw [default: auto]")
	fmt.Fprintln(os.Stderr)

	fmt.Fprintln(os.Stderr, "Verbosity Levels:")
	fmt.Fprintln(os.Stderr, "  (default)                      minimal request info with summary timings")
	fmt.Fprintln(os.Stderr, "  -v                             adds target/TLS summaries and timing diagram")
	fmt.Fprintln(os.Stderr, "  -vv                            includes full TLS certificates and headers")
	fmt.Fprintln(os.Stderr)

	fmt.Fprintln(os.Stderr, "Examples:")
	fmt.Fprintln(os.Stderr, "  wsstat wss://echo.example.com")
	fmt.Fprintln(os.Stderr, "  wsstat -t \"ping\" wss://echo.example.com")
	fmt.Fprintln(os.Stderr, "  wsstat --rpc-method eth_blockNumber wss://rpc.example.com/ws")
	fmt.Fprintln(os.Stderr, "  wsstat --subscribe --summary-interval 5s wss://stream.example.com/feed")
	fmt.Fprintln(os.Stderr, "  wsstat -H \"Authorization: Bearer TOKEN\" -H \"Origin: https://foo\" wss://api.example.com/ws")
	fmt.Fprintln(os.Stderr, "  wsstat --insecure -vv wss://self-signed.example.com")
}
