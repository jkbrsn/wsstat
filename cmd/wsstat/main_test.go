package main

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunIntegration(t *testing.T) {
	// Save original state
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	resetTestState := func() {
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

		// Reset all flag pointers
		noTLS = flag.Bool("no-tls", false, "")
		colorArg = flag.String("color", "auto", "")
		quiet = flag.Bool("q", false, "")
		showVersion = flag.Bool("version", false, "")
		textMessage = flag.String("text", "", "")
		rpcMethod = flag.String("rpc-method", "", "")
		formatOption = flag.String("format", "auto", "")
		subscribe = flag.Bool("subscribe", false, "")
		subscribeOnce = flag.Bool("subscribe-once", false, "")
		bufferSize = flag.Int("buffer", 0, "")
		summaryInterval = flag.Duration("summary-interval", 0, "")
		v1 = flag.Bool("v", false, "")
		v2 = flag.Bool("vv", false, "")

		countFlag = newTrackedIntFlag(1)
		headerArguments = headerList{}

		flag.Var(&countFlag, "count", "")
		flag.Var(&headerArguments, "H", "")
		flag.Var(&headerArguments, "header", "")
	}

	t.Run("version flag returns special error", func(t *testing.T) {
		resetTestState()
		os.Args = []string{"wsstat", "-version"}
		_ = flag.CommandLine.Parse(os.Args[1:])

		err := run()
		assert.ErrorIs(t, err, errVersionRequested)
	})

	t.Run("missing URL returns error", func(t *testing.T) {
		resetTestState()
		os.Args = []string{"wsstat"}
		_ = flag.CommandLine.Parse(os.Args[1:])

		err := run()
		assert.Error(t, err)
	})

	t.Run("invalid URL returns error", func(t *testing.T) {
		resetTestState()
		os.Args = []string{"wsstat", "ht!tp://invalid"}
		_ = flag.CommandLine.Parse(os.Args[1:])

		err := run()
		assert.Error(t, err)
	})

	t.Run("quiet and verbose conflict returns error", func(t *testing.T) {
		resetTestState()
		os.Args = []string{"wsstat", "-q", "-v", "example.com"}
		_ = flag.CommandLine.Parse(os.Args[1:])

		err := run()
		assert.Error(t, err)
	})

	t.Run("invalid header format returns error", func(t *testing.T) {
		resetTestState()
		os.Args = []string{"wsstat", "-H", "InvalidHeader", "example.com"}
		_ = flag.CommandLine.Parse(os.Args[1:])

		err := run()
		assert.Error(t, err)
	})

	// Note: We can't easily test successful runs without mocking the internal/app.Client
	// or setting up a real WebSocket server. The error path coverage above is sufficient
	// for integration testing at the cmd level.
}
