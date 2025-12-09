package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config holds all configuration parsed from command-line flags.
type Config struct {
	TargetURL       *url.URL
	Count           int
	Headers         []string
	Resolves        map[string]string // DNS resolution overrides: "host:port" → "address"
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
	Insecure        bool
}

// parseConfig parses command-line flags and returns a validated Config.
func parseConfig() (*Config, error) {
	flag.Parse()

	if *showVersion {
		fmt.Printf("Version: %s\n", version)
		return nil, errVersionRequested
	}

	// Unify verbosity flags (highest wins)
	verbosity := 0
	if *v2 {
		verbosity = 2
	} else if *v1 {
		verbosity = 1
	}

	if *quiet && verbosity > 0 {
		return nil, errors.New("-q cannot be combined with -v or -vv")
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
		Resolves:        resolveOverrides.Values(),
		RPCMethod:       *rpcMethod,
		TextMessage:     *textMessage,
		Subscribe:       *subscribe,
		SubscribeOnce:   *subscribeOnce,
		BufferSize:      *bufferSize,
		SummaryInterval: *summaryInterval,
		Format:          strings.ToLower(*formatOption),
		ColorMode:       strings.ToLower(*colorArg),
		Quiet:           *quiet,
		Verbosity:       verbosity,
		Insecure:        *insecure,
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
