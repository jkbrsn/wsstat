package main

import (
	"fmt"
	"io"
)

// revive:disable:line-length-limit aligned help text

// printTopUsage prints the top-level usage listing the two subcommands.
func printTopUsage(w io.Writer) {
	fmt.Fprintf(w, "wsstat %s\n", version)
	fmt.Fprintln(w, "Measure latency on WebSocket connections")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "USAGE:")
	fmt.Fprintln(w, "  wsstat <url>                    measure connection latency (bare form)")
	fmt.Fprintln(w, "  wsstat measure [options] <url>  measure connection latency")
	fmt.Fprintln(w, "  wsstat stream  [options] <url>  stream subscription events")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "COMMANDS:")
	fmt.Fprintln(w, "  measure   send ping/text/JSON-RPC and report timing (DNS, TCP, TLS, WS, RTT)")
	fmt.Fprintln(w, "  stream    keep the connection open and forward incoming frames")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  --version                       print program version and exit")
	fmt.Fprintln(w, "  -h, --help                      show this help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'wsstat measure -h' or 'wsstat stream -h' for command-specific flags.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  wsstat wss://echo.example.com")
	fmt.Fprintln(w, "  wsstat measure -t \"ping\" wss://echo.example.com")
	fmt.Fprintln(w, "  wsstat measure --rpc-method eth_blockNumber wss://rpc.example.com/ws")
	fmt.Fprintln(w, "  wsstat stream --summary-interval 5s wss://stream.example.com/feed")
	fmt.Fprintln(w, "  wsstat stream --once -o json wss://api.example.com/ws")
}

// printCommonFlags prints the flags shared by every subcommand.
func printCommonFlags(w io.Writer) {
	fmt.Fprintln(w, "Input (choose one):")
	fmt.Fprintln(w, "      --rpc-method <string>      JSON-RPC method name to send (id=1, jsonrpc=2.0)")
	fmt.Fprintln(w, "  -t, --text <string>            text message to send")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output:")
	fmt.Fprintln(w, "  -o, --output <string>          output contract: text, json, raw [default: text]")
	fmt.Fprintln(w, "      --body <string>            text body rendering: auto, compact [default: auto]")
	fmt.Fprintln(w, "      --clip                     clip each rendered line to terminal width (TTY only)")
	fmt.Fprintln(w, "  -q, --quiet                    suppress all output except the response")
	fmt.Fprintln(w, "  -v                             increase verbosity (level 1)")
	fmt.Fprintln(w, "  -vv                            increase verbosity (level 2)")
	fmt.Fprintln(w, "      --color <string>           color output: auto, always, never [default: auto]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Note: --body, --clip, -q, -v, -vv apply only to -o text; -o json is schema-stable.")
	fmt.Fprintln(w, "        -o raw with --rpc-method emits compact JSON (the frame is decoded before output).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Connection:")
	fmt.Fprintln(w, "  -H, --header <string>          HTTP header to include (repeatable; \"Key: Value\")")
	fmt.Fprintln(w, "      --resolve <string>         resolve host:port to address (repeatable; \"HOST:PORT:ADDRESS\")")
	fmt.Fprintln(w, "  -k, --insecure                 skip TLS certificate verification (use with caution)")
	fmt.Fprintln(w, "      --timeout <duration>       read/dial timeout (e.g., 30s, 1m) [default: 5s]")
	fmt.Fprintln(w, "      --close-timeout <duration> max wait for the peer's close echo [default: 3s; capped at 5s]")
}

// printMeasureUsage prints usage for the measure subcommand.
func printMeasureUsage(w io.Writer) {
	fmt.Fprintln(w, "wsstat measure - measure WebSocket connection latency")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "USAGE:")
	fmt.Fprintln(w, "  wsstat measure [options] <url>")
	fmt.Fprintln(w, "  wsstat [options] <url>   (bare form)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Measure:")
	fmt.Fprintln(w, "  -c, --count <int>              number of interactions to perform [default: 1; >= 1]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Note: timing is aggregated across all interactions; the response shown is the first.")
	fmt.Fprintln(w)
	printCommonFlags(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Verbosity Levels (text output):")
	fmt.Fprintln(w, "  (default)                      minimal request info with summary timings")
	fmt.Fprintln(w, "  -v                             adds target/TLS summaries and timing diagram")
	fmt.Fprintln(w, "  -vv                            includes full TLS certificates and headers")
}

// printStreamUsage prints usage for the stream subcommand.
func printStreamUsage(w io.Writer) {
	fmt.Fprintln(w, "wsstat stream - stream WebSocket subscription events")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "USAGE:")
	fmt.Fprintln(w, "  wsstat stream [options] <url>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Stream:")
	fmt.Fprintln(w, "  -c, --count <int>              number of events to receive [default: 0 = unlimited]")
	fmt.Fprintln(w, "      --once                     exit after the first event")
	fmt.Fprintln(w, "  -b, --buffer <int>             delivery buffer size in messages [default: 0]")
	fmt.Fprintln(w, "      --summary-interval <duration>")
	fmt.Fprintln(w, "                                 print stat summaries every interval (e.g., 5s, 1m) [default: disabled]")
	fmt.Fprintln(w)
	printCommonFlags(w)
}
