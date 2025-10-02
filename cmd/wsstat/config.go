package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config holds all configuration parsed from command-line flags.
type Config struct {
	TargetURL       *url.URL
	Count           int
	Headers         []string
	RPCMethod       string
	TextMessage     string
	Subscribe       bool
	SubscribeOnce   bool
	BufferSize      int
	SummaryInterval time.Duration
	Format          string
	ColorMode       string
	Quiet           bool
	Verbosity       int
}

// parseConfig parses command-line flags and returns a validated Config.
func parseConfig() (*Config, error) {
	flag.Parse()

	if *showVersion {
		fmt.Printf("Version: %s\n", version)
		return nil, errVersionRequested
	}

	if *quiet && verbosityLevel.Value() > 0 {
		return nil, errors.New("-q cannot be combined with -v")
	}

	if *textMessage != "" && *rpcMethod != "" {
		return nil, errors.New("mutually exclusive messaging flags")
	}

	args := flag.Args()
	if len(args) != 1 {
		return nil, errors.New("invalid number of arguments")
	}

	switch strings.ToLower(*colorArg) {
	case "auto", "always", "never":
		// valid
	default:
		return nil, errors.New("-color must be auto, always, or never")
	}

	targetURL, err := parseWSURI(args[0])
	if err != nil {
		return nil, fmt.Errorf("error parsing input URI: %w", err)
	}

	effectiveCount := resolveCountValue(*subscribe, *subscribeOnce)

	cfg := &Config{
		TargetURL:       targetURL,
		Count:           effectiveCount,
		Headers:         headerArguments.Values(),
		RPCMethod:       *rpcMethod,
		TextMessage:     *textMessage,
		Subscribe:       *subscribe,
		SubscribeOnce:   *subscribeOnce,
		BufferSize:      *bufferSize,
		SummaryInterval: *summaryInterval,
		Format:          strings.ToLower(*formatOption),
		ColorMode:       strings.ToLower(*colorArg),
		Quiet:           *quiet,
		Verbosity:       verbosityLevel.Value(),
	}

	return cfg, nil
}

// parseWSURI parses the rawURI string into a URL object.
func parseWSURI(rawURI string) (*url.URL, error) {
	uri := rawURI
	if !strings.Contains(rawURI, "://") {
		scheme := "wss://"
		if *noTLS {
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

// resolveCountValue returns the count value based on the flags.
//
// revive:disable:flag-parameter allow
func resolveCountValue(subscribe, subscribeOnce bool) int {
	if !subscribeOnce && (subscribe && !countFlag.WasSet()) {
		return 0
	}
	return countFlag.Value()
}

// errVersionRequested is returned when -version flag is used.
var errVersionRequested = errors.New("version requested")

// onlyRune returns true if the string consists solely of the provided rune.
func onlyRune(s string, r rune) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch != r {
			return false
		}
	}
	return true
}

// preprocessVerbosityArgs rewrites os.Args so that shorthand -v/-vv translates to
// canonical -v=N forms before flag parsing. This lets the default flag package
// treat -v as a repeatable count.
func preprocessVerbosityArgs() {
	if len(os.Args) <= 1 {
		return
	}

	filtered := make([]string, 0, len(os.Args)-1)
	for _, arg := range os.Args[1:] {
		switch {
		case arg == "-v" || arg == "--verbose":
			filtered = append(filtered, "-v=1")
		case strings.HasPrefix(arg, "-v="):
			filtered = append(filtered, arg)
		case strings.HasPrefix(arg, "-vv") && onlyRune(arg[1:], 'v'):
			filtered = append(filtered, fmt.Sprintf("-v=%d", len(arg)-1))
		default:
			filtered = append(filtered, arg)
		}
	}

	os.Args = append([]string{os.Args[0]}, filtered...)
}
