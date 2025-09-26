package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// trackedIntFlag is a flag.Value implementation that tracks whether a flag was set.
type trackedIntFlag struct {
	value int
	set   bool
}

// Set converts the string value to an integer and stores it.
func (f *trackedIntFlag) Set(s string) error {
	v, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	f.value = v
	f.set = true
	return nil
}

// String returns the string representation of the flag value.
func (f *trackedIntFlag) String() string {
	return strconv.Itoa(f.value)
}

// Value returns the integer value of the flag.
func (f *trackedIntFlag) Value() int {
	return f.value
}

// WasSet returns true if the flag was set.
func (f *trackedIntFlag) WasSet() bool {
	return f.set
}

// newTrackedIntFlag creates a new trackedIntFlag with the given default value.
func newTrackedIntFlag(defaultValue int) trackedIntFlag {
	return trackedIntFlag{value: defaultValue}
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

// resolveCountValue returns the count value based on the flags.
func resolveCountValue(subscribe, subscribeOnce bool) int {
	if !countFlag.WasSet() && subscribe && !subscribeOnce {
		return 0
	}
	return countFlag.Value()
}
