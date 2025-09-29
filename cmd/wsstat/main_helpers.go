package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

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

// parseValidateInput parses and validates the flags and input passed to the program.
func parseValidateInput() (*url.URL, error) {
	flag.Parse()

	if *showVersion {
		fmt.Printf("Version: %s\n", version)
		os.Exit(0) //revive:disable:deep-exit allow here
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

	u, err := parseWSURI(args[0])
	if err != nil {
		return nil, fmt.Errorf("error parsing input URI: %v", err)
	}

	return u, nil
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

// resolveCountValue returns the count value based on the flags.
//
// revive:disable:flag-parameter allow
func resolveCountValue(subscribe, subscribeOnce bool) int {
	if !subscribeOnce && (subscribe && !countFlag.WasSet()) {
		return 0
	}
	return countFlag.Value()
}
