// Package main parses and validates the flags and input passed to the program,
// and then measures the latency of a WebSocket connection using the internal client.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jkbrsn/wsstat/internal/app"
)

var (
	// Input
	burst        = flag.Int("burst", 1, "number of messages to send in a burst")
	inputHeaders = flag.String("headers", "",
		"comma-separated headers for the connection establishing request")
	jsonMethod    = flag.String("json", "", "a single JSON RPC method to send")
	textMessage   = flag.String("text", "", "a text message to send")
	subscribe     = flag.Bool("subscribe", false, "keep the connection open and stream events")
	subscribeOnce = flag.Bool("subscribe-once", false, "subscribe and exit after the first event")
	subBuffer     = flag.Int("subscription-buffer", 0, "override subscription delivery buffer size")
	subInterval   = flag.Duration("subscription-interval", 0,
		"print subscription summaries every interval; 0 disables")
	// Output
	rawOutput   = flag.Bool("raw", false, "let printed output be the raw data of the response")
	showVersion = flag.Bool("version", false, "print the program version")
	version     = "unknown"
	// Protocol
	insecure = flag.Bool("insecure", false,
		"open an insecure WS connection in case of missing scheme in the input")
	// Verbosity
	basic   = flag.Bool("b", false, "print basic output")
	quiet   = flag.Bool("q", false, "quiet all output but the response")
	verbose = flag.Bool("v", false, "print verbose output")
)

func init() {
	// Define custom usage message
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage:  wsstat [options] <url>")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Mutually exclusive input options:")
		fmt.Fprintln(os.Stderr, "  -json  "+flag.Lookup("json").Usage)
		fmt.Fprintln(os.Stderr, "  -text  "+flag.Lookup("text").Usage)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Mutually exclusive output options:")
		fmt.Fprintln(os.Stderr, "  -b  "+flag.Lookup("b").Usage)
		fmt.Fprintln(os.Stderr, "  -v  "+flag.Lookup("v").Usage)
		fmt.Fprintln(os.Stderr, "  -q  "+flag.Lookup("q").Usage)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Other options:")
		fmt.Fprintln(os.Stderr, "  -burst     "+flag.Lookup("burst").Usage)
		fmt.Fprintln(os.Stderr, "  -subscribe "+flag.Lookup("subscribe").Usage)
		fmt.Fprintln(os.Stderr, "  -subscribe-once "+flag.Lookup("subscribe-once").Usage)
		fmt.Fprintln(os.Stderr, "  -subscription-buffer  "+flag.Lookup("subscription-buffer").Usage)
		fmt.Fprintln(os.Stderr,
			"  -subscription-interval "+flag.Lookup("subscription-interval").Usage)
		fmt.Fprintln(os.Stderr, "  -headers   "+flag.Lookup("headers").Usage)
		fmt.Fprintln(os.Stderr, "  -raw       "+flag.Lookup("raw").Usage)
		fmt.Fprintln(os.Stderr, "  -insecure  "+flag.Lookup("insecure").Usage)
		fmt.Fprintln(os.Stderr, "  -version   "+flag.Lookup("version").Usage)
	}
}

func main() {
	targetURL, err := parseValidateInput()
	if err != nil {
		fmt.Printf("Error parsing input: %v\n\n", err)
		flag.Usage()
		os.Exit(1)
	}

	ws := app.Client{
		Burst:                *burst,
		InputHeaders:         *inputHeaders,
		JSONMethod:           *jsonMethod,
		TextMessage:          *textMessage,
		RawOutput:            *rawOutput,
		ShowVersion:          *showVersion,
		Version:              version,
		Insecure:             *insecure,
		Basic:                *basic,
		Quiet:                *quiet,
		Verbose:              *verbose,
		Subscribe:            *subscribe,
		SubscribeOnce:        *subscribeOnce,
		SubscriptionBuffer:   *subBuffer,
		SubscriptionInterval: *subInterval,
	}
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

	err = ws.MeasureLatency(targetURL)
	if err != nil {
		fmt.Printf("Error measuring latency: %v\n", err)
		os.Exit(1)
	}

	// Print the results if there is no expected response or if the quiet flag is not set
	if !*quiet {
		// Print details of the request
		err = ws.PrintRequestDetails()
		if err != nil {
			fmt.Printf("Error printing request details: %v\n", err)
			os.Exit(1)
		}

		// Print the timing results
		err = ws.PrintTimingResults(targetURL)
		if err != nil {
			fmt.Printf("Error printing timing results: %v\n", err)
			os.Exit(1)
		}
	}

	// Print the response, if there is one
	ws.PrintResponse()
}

// parseValidateInput parses and validates the flags and input passed to the program.
func parseValidateInput() (*url.URL, error) {
	flag.Parse()

	if *showVersion {
		fmt.Printf("Version: %s\n", version)
		os.Exit(0) //revive:disable:deep-exit allow here
	}

	if *basic && *verbose || *basic && *quiet || *verbose && *quiet {
		return nil, errors.New("mutually exclusive verbosity flags")
	}

	if *textMessage != "" && *jsonMethod != "" {
		return nil, errors.New("mutually exclusive messaging flags")
	}

	args := flag.Args()
	if len(args) != 1 {
		return nil, errors.New("invalid number of arguments")
	}

	u, err := parseWSURI(args[0])
	if err != nil {
		return nil, fmt.Errorf("error parsing input URI: %v", err)
	}

	return u, nil
}

// parseWSURI parses the rawURI string into a URL object.
func parseWSURI(rawURI string) (*url.URL, error) {
	uri := rawURI
	if !strings.Contains(rawURI, "://") {
		scheme := "wss://"
		if *insecure {
			scheme = "ws://"
		}
		uri = scheme + rawURI
	}

	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	return u, nil
}
