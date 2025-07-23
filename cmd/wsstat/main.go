package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jkbrsn/go-wsstat"
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
	url, err := parseValidateInput()
	if err != nil {
		fmt.Printf("Error parsing input: %v\n\n", err)
		flag.Usage()
		os.Exit(1)
	}

	header := parseHeaders(*inputHeaders)
	result, response, err := measureLatency(url, header)
	if err != nil {
		fmt.Printf("Error measuring latency: %v\n", err)
		os.Exit(1)
	}

	// Print the results if there is no expected response or if the quiet flag is not set
	if !*quiet {
		// Print details of the request
		printRequestDetails(*result)

		// Print the timing results
		printTimingResults(url, *result)
	}

	// Print the response, if there is one
	printResponse(response)
}

// colorWSOrange returns the text with a custom orange color.
// The color is from the WS logo, #ff6600 is its hex code.
func colorWSOrange(text string) string {
	return customColor(255, 102, 0, text)
}

// colorTeaGreen returns the text with a custom tea green color.
// The color has hex code #d3f9b5.
func colorTeaGreen(text string) string {
	return customColor(211, 249, 181, text)
}

// customColor returns the text with a custom RGB color.
func customColor(r, g, b int, text string) string {
	return fmt.Sprintf("\033[38;2;%d;%d;%dm%s\033[0m", r, g, b, text)
}

// formatPadLeft formats the duration to a string with padding on the left.
func formatPadLeft(d time.Duration) string {
	return fmt.Sprintf("%7dms", int(d/time.Millisecond))
}

// formatPadRight formats the duration to a string with padding on the right.
func formatPadRight(d time.Duration) string {
	return fmt.Sprintf("%-8s", strconv.Itoa(int(d/time.Millisecond))+"ms")
}

// handleConnectionError prints the error message and exits the program.
func handleConnectionError(err error, url string) error {
	if strings.Contains(err.Error(), "tls: first record does not look like a TLS handshake") {
		return fmt.Errorf("error establishing secure WS connection to '%s': %v", url, err)
	}
	return fmt.Errorf("error establishing WS connection to '%s': %v", url, err)
}

// measureLatency measures the latency of the WebSocket connection, applying different methods
// based on the flags passed to the program.
func measureLatency(url *url.URL, header http.Header) (*wsstat.Result, any, error) {
	var result *wsstat.Result
	var response any
	var err error
	if *textMessage != "" {
		msgs := make([]string, *burst)
		for i := 0; i < *burst; i++ {
			msgs[i] = *textMessage
		}
		result, response, err = wsstat.MeasureLatencyBurst(url, msgs, header)
		if err != nil {
			return nil, nil, handleConnectionError(err, url.String())
		}
		if responseArray, ok := response.([]string); ok && len(responseArray) > 0 {
			response = responseArray[0]
		}
		if !*rawOutput {
			// Automatically decode JSON messages
			decodedMessage := make(map[string]any)
			responseStr, ok := response.(string)
			if ok {
				err := json.Unmarshal([]byte(responseStr), &decodedMessage)
				if err != nil {
					return nil, nil, fmt.Errorf("error unmarshalling JSON message: %v", err)
				}
				response = decodedMessage
			}
		}
	} else if *jsonMethod != "" {
		msg := struct {
			Method     string `json:"method"`
			ID         string `json:"id"`
			RPCVersion string `json:"jsonrpc"`
		}{
			Method:     *jsonMethod,
			ID:         "1",
			RPCVersion: "2.0",
		}
		msgs := make([]any, *burst)
		for i := 0; i < *burst; i++ {
			msgs[i] = msg
		}
		result, response, err = wsstat.MeasureLatencyJSONBurst(url, msgs, header)
		if err != nil {
			return nil, nil, handleConnectionError(err, url.String())
		}
	} else {
		result, err = wsstat.MeasureLatencyPingBurst(url, *burst, header)
		if err != nil {
			return nil, nil, handleConnectionError(err, url.String())
		}
	}
	res := result
	return res, response, nil
}

// parseHeaders parses comma separated headers into an HTTP header.
func parseHeaders(headers string) http.Header {
	header := http.Header{}
	if headers != "" {
		headerParts := strings.Split(headers, ",")
		for _, part := range headerParts {
			parts := strings.SplitN(part, ":", 2)
			if len(parts) != 2 {
				log.Fatalf("Invalid header format: %s", part)
			}
			header.Add(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	return header
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

// printRequestDetails prints the headers of the WebSocket connection to the terminal.
func printRequestDetails(result wsstat.Result) {
	fmt.Println()

	// Print basic output
	if *basic {
		fmt.Printf("%s: %s\n", colorTeaGreen("URL"), result.URL.Hostname())
		if len(result.IPs) > 0 {
			fmt.Printf("%s:  %s\n", colorTeaGreen("IP"), result.IPs[0])
		}
		return
	}

	// Print verbose output
	if *verbose {
		fmt.Println(colorWSOrange("Target"))
		fmt.Printf("  %s:  %s\n", colorTeaGreen("URL"), result.URL.Hostname())
		// Loop in case there are multiple IPs with the target
		for _, ip := range result.IPs {
			fmt.Printf("  %s: %s\n", colorTeaGreen("IP"), ip)
		}
		fmt.Printf("  %s: %d\n", colorTeaGreen("Messages sent:"), result.MessageCount)
		fmt.Println()
		if result.TLSState != nil {
			fmt.Println(colorWSOrange("TLS"))
			fmt.Printf("  %s: %s\n", colorTeaGreen("Version"), tls.VersionName(result.TLSState.Version))
			fmt.Printf("  %s: %s\n", colorTeaGreen("Cipher Suite"), tls.CipherSuiteName(result.TLSState.CipherSuite))

			// Print the certificate details
			for i, cert := range result.TLSState.PeerCertificates {
				fmt.Printf("  %s: %d\n", colorTeaGreen("Certificate"), i+1)
				fmt.Printf("    Subject: %s\n", cert.Subject)
				fmt.Printf("    Issuer: %s\n", cert.Issuer)
				fmt.Printf("    Not Before: %s\n", cert.NotBefore)
				fmt.Printf("    Not After: %s\n", cert.NotAfter)
			}
			fmt.Println()
		}
		fmt.Println(colorWSOrange("Request headers"))
		for key, values := range result.RequestHeaders {
			fmt.Printf("  %s: %s\n", colorTeaGreen(key), strings.Join(values, ", "))
		}
		fmt.Println(colorWSOrange("Response headers"))
		for key, values := range result.ResponseHeaders {
			fmt.Printf("  %s: %s\n", colorTeaGreen(key), strings.Join(values, ", "))
		}
		return
	}

	// Print standard output
	fmt.Printf("%s: %s\n", colorWSOrange("Target"), result.URL.Hostname())
	for _, values := range result.IPs {
		fmt.Printf("%s: %s\n", colorWSOrange("IP"), values)
	}
	fmt.Printf("%s: %d\n", colorWSOrange("Messages sent:"), result.MessageCount)
	for key, values := range result.RequestHeaders {
		if key == "Sec-WebSocket-Version" {
			fmt.Printf("%s: %s\n", colorWSOrange("WS version"), strings.Join(values, ", "))
		}
	}
	if result.TLSState != nil {
		fmt.Printf("%s: %s\n", colorWSOrange("TLS version"), tls.VersionName(result.TLSState.Version))
	}
}

// printResponse prints the response to the terminal, if there is a response.
func printResponse(response any) {
	if response == nil {
		return
	}
	baseMessage := colorWSOrange("Response") + ": "
	if *quiet {
		baseMessage = ""
	} else {
		fmt.Println()
	}
	if *rawOutput {
		// If raw output is requested, print the raw data before trying to assert any types
		fmt.Printf("%s%v\n", baseMessage, response)
	} else if responseMap, ok := response.(map[string]any); ok {
		// If JSON in request, print response as JSON
		if _, isJSON := responseMap["jsonrpc"]; isJSON || *jsonMethod != "" {
			responseJSON, err := json.Marshal(responseMap)
			if err != nil {
				fmt.Printf("Could not marshal response to JSON. Response: %v, error: %v", responseMap, err)
				return
			}
			fmt.Printf("%s%s\n", baseMessage, responseJSON)
		} else {
			fmt.Printf("%s%v\n", baseMessage, responseMap)
		}
	} else if responseArray, ok := response.([]any); ok {
		fmt.Printf("%s%v\n", baseMessage, responseArray)
	} else if responseBytes, ok := response.([]byte); ok {
		fmt.Printf("%s%v\n", baseMessage, responseBytes)
	}
	if !*quiet {
		fmt.Println()
	}
}

// printTimingResults prints the WebSocket statistics to the terminal.
func printTimingResults(url *url.URL, result wsstat.Result) {
	if *basic {
		printTimingResultsBasic(result)
	} else {
		printTimingResultsTiered(url, result)
	}
}

// printTimingResultsBasic formats and prints only the most basic WebSocket statistics.
func printTimingResultsBasic(result wsstat.Result) {
	fmt.Println()
	rttString := "Round-trip time"
	if *burst > 1 {
		rttString = "Mean round-trip time"
	}
	msgCountString := "message"
	if result.MessageCount > 1 {
		msgCountString = "messages"
	}
	fmt.Printf(
		"%s: %s (%d %s)\n",
		rttString,
		colorWSOrange(strconv.FormatInt(result.MessageRTT.Milliseconds(), 10)+"ms"),
		result.MessageCount,
		msgCountString)
	fmt.Printf(
		"%s: %s\n",
		"Total time",
		colorWSOrange(strconv.FormatInt(result.TotalTime.Milliseconds(), 10)+"ms"))
	fmt.Println()
}

// printTimingResultsTiered formats and prints the WebSocket statistics to the terminal in a tiered fashion.
func printTimingResultsTiered(url *url.URL, result wsstat.Result) {
	fmt.Println()
	switch url.Scheme {
	case "wss":
		fmt.Fprintf(os.Stdout, wssPrintTemplate,
			colorTeaGreen(formatPadLeft(result.DNSLookup)),
			colorTeaGreen(formatPadLeft(result.TCPConnection)),
			colorTeaGreen(formatPadLeft(result.TLSHandshake)),
			colorTeaGreen(formatPadLeft(result.WSHandshake)),
			colorTeaGreen(formatPadLeft(result.MessageRTT)),
			colorTeaGreen(formatPadRight(result.DNSLookupDone)),
			colorTeaGreen(formatPadRight(result.TCPConnected)),
			colorTeaGreen(formatPadRight(result.TLSHandshakeDone)),
			colorTeaGreen(formatPadRight(result.WSHandshakeDone)),
			//formatPadRight(result.FirstMessageResponse), // Skipping due to ConnectionClose skip
			colorWSOrange(formatPadRight(result.TotalTime)),
		)
	case "ws":
		fmt.Fprintf(os.Stdout, wsPrintTemplate,
			colorTeaGreen(formatPadLeft(result.DNSLookup)),
			colorTeaGreen(formatPadLeft(result.TCPConnection)),
			colorTeaGreen(formatPadLeft(result.WSHandshake)),
			colorTeaGreen(formatPadLeft(result.MessageRTT)),
			colorTeaGreen(formatPadRight(result.DNSLookupDone)),
			colorTeaGreen(formatPadRight(result.TCPConnected)),
			colorTeaGreen(formatPadRight(result.WSHandshakeDone)),
			//formatPadRight(result.FirstMessageResponse), // Skipping due to ConnectionClose skip
			colorWSOrange(formatPadRight(result.TotalTime)),
		)
	}
	fmt.Println()
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
