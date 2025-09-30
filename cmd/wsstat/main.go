// Package main parses CLI input for wsstat, validates it, and delegates to the
// internal client implementation.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
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
	noTLS    = flag.Bool("no-tls", false, "assume ws:// when input URL lacks scheme (default wss://)")
	colorArg = flag.String("color", "auto", "color output: auto, always, or never")

	// Verbosity
	quiet          = flag.Bool("q", false, "quiet all output but the response")
	verbosityLevel = newVerbosityCounter()
)

func init() {
	preprocessVerbosityArgs()

	flag.Var(&countFlag, "count", "number of interactions to perform; 0 means unlimited when subscribing")
	flag.Var(&headerArguments, "H", "HTTP header to include with the request (repeatable; format: Key: Value)")
	flag.Var(&headerArguments, "header", "HTTP header to include with the request (repeatable; format: Key: Value)")
	flag.Var(verbosityLevel, "v", "increase verbosity; repeatable (e.g., -v -v) or use -v=N")

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage:  wsstat [options] <url>")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Measure WebSocket latency or stream subscription events.")
		fmt.Fprintln(os.Stderr, "If the URL omits a scheme, wsstat assumes wss:// unless -no-tls is provided.")
		fmt.Fprintln(os.Stderr)

		fmt.Fprintln(os.Stderr, "General:")
		fmt.Fprintf(os.Stderr, "  -count int           %s (default 1; defaults to unlimited when subscribing)\n", flag.Lookup("count").Usage)
		fmt.Fprintln(os.Stderr)

		fmt.Fprintln(os.Stderr, "Input (choose one):")
		fmt.Fprintf(os.Stderr, "  -rpc-method string   %s\n", flag.Lookup("rpc-method").Usage)
		fmt.Fprintf(os.Stderr, "  -text string         %s\n", flag.Lookup("text").Usage)
		fmt.Fprintln(os.Stderr)

		fmt.Fprintln(os.Stderr, "Subscription:")
		fmt.Fprintf(os.Stderr, "  -subscribe           %s\n", flag.Lookup("subscribe").Usage)
		fmt.Fprintf(os.Stderr, "  -subscribe-once      %s\n", flag.Lookup("subscribe-once").Usage)
		fmt.Fprintf(os.Stderr, "  -buffer int          %s (default %d)\n", flag.Lookup("buffer").Usage, *bufferSize)
		fmt.Fprintf(os.Stderr, "  -summary-interval    %s\n", flag.Lookup("summary-interval").Usage)
		fmt.Fprintln(os.Stderr)

		fmt.Fprintln(os.Stderr, "Connection:")
		fmt.Fprintf(os.Stderr, "  -H / -header string  %s\n", flag.Lookup("H").Usage)
		fmt.Fprintf(os.Stderr, "  -no-tls              %s\n", flag.Lookup("no-tls").Usage)
		fmt.Fprintf(os.Stderr, "  -color string        %s (auto|always|never; default %q)\n", flag.Lookup("color").Usage, *colorArg)
		fmt.Fprintln(os.Stderr)

		fmt.Fprintln(os.Stderr, "Output:")
		fmt.Fprintf(os.Stderr, "  -q                   %s\n", flag.Lookup("q").Usage)
		fmt.Fprintf(os.Stderr, "  -v                   %s\n", flag.Lookup("v").Usage)
		fmt.Fprintf(os.Stderr, "  -format string       %s (default %q)\n", flag.Lookup("format").Usage, *formatOption)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Verbosity:")
		fmt.Fprintln(os.Stderr, "  default  minimal request info with summary timings")
		fmt.Fprintln(os.Stderr, "  -v       adds target/TLS summaries and timing diagram")
		fmt.Fprintln(os.Stderr, "  -vv      includes full TLS certificates and headers")
		fmt.Fprintln(os.Stderr)

		fmt.Fprintln(os.Stderr, "Misc:")
		fmt.Fprintf(os.Stderr, "  -version             %s\n", flag.Lookup("version").Usage)

		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  wsstat wss://echo.example.com")
		fmt.Fprintln(os.Stderr, "  wsstat -text \"ping\" wss://echo.example.com")
		fmt.Fprintln(os.Stderr, "  wsstat -rpc-method eth_blockNumber wss://rpc.example.com/ws")
		fmt.Fprintln(os.Stderr, "  wsstat -subscribe -count 1 wss://stream.example.com/feed")
		fmt.Fprintln(os.Stderr, "  wsstat -subscribe -summary-interval 5s wss://stream.example.com/feed")
		fmt.Fprintln(os.Stderr, "  wsstat -H \"Authorization: Bearer TOKEN\" -H \"Origin: https://foo\" wss://api.example.com/ws")
	}
}

// revive:enable:line-length-limit

func main() {
	targetURL, err := parseValidateInput()
	if err != nil {
		fmt.Printf("Error parsing input: %v\n\n", err)
		flag.Usage()
		os.Exit(1)
	}

	effectiveCount := resolveCountValue(*subscribe, *subscribeOnce)

	ws := app.NewClient(
		app.WithCount(effectiveCount),
		app.WithHeaders(headerArguments.Values()),
		app.WithRPCMethod(*rpcMethod),
		app.WithTextMessage(*textMessage),
		app.WithFormat(strings.ToLower(*formatOption)),
		app.WithColorMode(strings.ToLower(*colorArg)),
		app.WithQuiet(*quiet),
		app.WithVerbosity(verbosityLevel.Value()),
		app.WithSubscription(*subscribe),
		app.WithSubscriptionOnce(*subscribeOnce),
		app.WithBuffer(*bufferSize),
		app.WithSummaryInterval(*summaryInterval),
	)

	err = ws.Validate()
	if err != nil {
		fmt.Printf("Error in input settings: %v\n", err)
		os.Exit(1)
	}

	if *subscribeOnce {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		err = ws.StreamSubscriptionOnce(ctx, targetURL)
		if err != nil {
			fmt.Printf("Error streaming subscription once: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *subscribe {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		err = ws.StreamSubscription(ctx, targetURL)
		if err != nil {
			fmt.Printf("Error streaming subscription: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	result, err := ws.MeasureLatency(ctx, targetURL)
	if err != nil {
		fmt.Printf("Error measuring latency: %v\n", err)
		os.Exit(1)
	}

	if !*quiet {
		if err = ws.PrintRequestDetails(result); err != nil {
			fmt.Printf("Error printing request details: %v\n", err)
			os.Exit(1)
		}

		if err = ws.PrintTimingResults(targetURL, result); err != nil {
			fmt.Printf("Error printing timing results: %v\n", err)
			os.Exit(1)
		}
	}

	ws.PrintResponse(result)
}
