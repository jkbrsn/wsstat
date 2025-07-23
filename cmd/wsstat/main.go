// Package main parses and validates the flags and input passed to the program,
// and then measures the latency of a WebSocket connection using the internal client.
package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jkbrsn/wsstat/internal/client"
)

var (
	wssPrintTemplate = `` +
		`  DNS Lookup    TCP Connection    TLS Handshake    WS Handshake    Message RTT` + "\n" +
		`|%s  |      %s  |     %s  |    %s  |   %s  |` + "\n" +
		`|           |                 |                |               |              |` + "\n" +
		`|  DNS lookup:%s        |                |               |              |` + "\n" +
		`|                 TCP connected:%s       |               |              |` + "\n" +
		`|                                       TLS done:%s      |              |` + "\n" +
		`|                                                        WS done:%s     |` + "\n" +
		`-                                                                         Total:%s` + "\n"

	wsPrintTemplate = `` +
		`  DNS Lookup    TCP Connection    WS Handshake    Message RTT` + "\n" +
		`|%s  |      %s  |    %s  |  %s   |` + "\n" +
		`|           |                 |               |              |` + "\n" +
		`|  DNS lookup:%s        |               |              |` + "\n" +
		`|                 TCP connected:%s      |              |` + "\n" +
		`|                                       WS done:%s     |` + "\n" +
		`-                                                        Total:%s` + "\n"
)

var (
	// Input
	burst        = flag.Int("burst", 1, "number of messages to send in a burst")
	inputHeaders = flag.String("headers", "", "comma-separated headers for the connection establishing request")
	jsonMethod   = flag.String("json", "", "a single JSON RPC method to send ")
	textMessage  = flag.String("text", "", "a text message to send")
	// Output
	rawOutput   = flag.Bool("raw", false, "let printed output be the raw data of the response")
	showVersion = flag.Bool("version", false, "print the program version")
	version     = "unknown"
	// Protocol
	insecure = flag.Bool("insecure", false, "open an insecure WS connection in case of missing scheme in the input")
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
		fmt.Fprintln(os.Stderr, "  -headers   "+flag.Lookup("headers").Usage)
		fmt.Fprintln(os.Stderr, "  -raw       "+flag.Lookup("raw").Usage)
		fmt.Fprintln(os.Stderr, "  -insecure  "+flag.Lookup("insecure").Usage)
		fmt.Fprintln(os.Stderr, "  -version   "+flag.Lookup("version").Usage)
	}
}

func main() {
	// TODO: move validation to the client
	url, err := parseValidateInput()
	if err != nil {
		fmt.Printf("Error parsing input: %v\n\n", err)
		flag.Usage()
		os.Exit(1)
	}

	client := client.WSClient{
		Burst:        *burst,
		InputHeaders: *inputHeaders,
		JsonMethod:   *jsonMethod,
		TextMessage:  *textMessage,
		RawOutput:    *rawOutput,
		ShowVersion:  *showVersion,
		Version:      version,
		Insecure:     *insecure,
		Basic:        *basic,
		Quiet:        *quiet,
		Verbose:      *verbose,
	}

	// TODO: store response in the client instead?
	response, err := client.MeasureLatency(url)
	if err != nil {
		fmt.Printf("Error measuring latency: %v\n", err)
		os.Exit(1)
	}

	// Print the results if there is no expected response or if the quiet flag is not set
	if !*quiet {
		// Print details of the request
		err := client.PrintRequestDetails()
		if err != nil {
			fmt.Printf("Error printing request details: %v\n", err)
			os.Exit(1)
		}

		// Print the timing results
		client.PrintTimingResults(url)
	}

	// Print the response, if there is one
	client.PrintResponse(response)
}

// parseValidateInput parses and validates the flags and input passed to the program.
func parseValidateInput() (*url.URL, error) {
	flag.Parse()

	if *showVersion {
		fmt.Printf("Version: %s\n", version)
		os.Exit(0)
	}

	if *basic && *verbose || *basic && *quiet || *verbose && *quiet {
		return nil, fmt.Errorf("mutually exclusive verbosity flags")
	}

	if *textMessage != "" && *jsonMethod != "" {
		return nil, fmt.Errorf("mutually exclusive messaging flags")
	}

	args := flag.Args()
	if len(args) != 1 {
		return nil, fmt.Errorf("invalid number of arguments")
	}

	url, err := parseWSURI(args[0])
	if err != nil {
		return nil, fmt.Errorf("error parsing input URI: %v", err)
	}

	if *burst > 1 {
		wssPrintTemplate = strings.Replace(wssPrintTemplate, "Message RTT", "Mean Message RTT", 1)
		wsPrintTemplate = strings.Replace(wsPrintTemplate, "Message RTT", "Mean Message RTT", 1)
	}

	return url, nil
}

// parseWSURI parses the rawURI string into a URL object.
func parseWSURI(rawURI string) (*url.URL, error) {
	if !strings.Contains(rawURI, "://") {
		scheme := "wss://"
		if *insecure {
			scheme = "ws://"
		}
		rawURI = scheme + rawURI
	}

	url, err := url.Parse(rawURI)
	if err != nil {
		return nil, err
	}

	return url, nil
}
