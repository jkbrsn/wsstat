// Package main provides examples of how to use the wsstat package.
package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"

	"github.com/gorilla/websocket"
	"github.com/jkbrsn/wsstat/v2"
)

func main() {
	args := os.Args
	if len(args) < 2 {
		log.Fatal("Usage: go run main.go URL")
	}
	rawURL := args[1]

	u, err := url.Parse(rawURL)
	if err != nil {
		log.Fatalf("Failed to parse URL: %v", err)
	}

	// Measure latency with one of the convenience functions
	var msg = "Hello, WebSocket!"
	result, p, err := wsstat.MeasureLatency(u, msg, http.Header{})
	if err != nil {
		log.Fatalf("Failed to measure latency: %v", err)
	}
	fmt.Printf("Basic example\nResponse: %s\n\nResult:\n%+v\n", p, result)

	// Measure latency with more control over the steps in the process by using the WSStat instance
	ws := wsstat.New()
	defer ws.Close()

	if err := ws.Dial(u, http.Header{}); err != nil {
		log.Fatalf("Failed to establish WebSocket connection: %v", err)
	}

	ws.WriteMessage(websocket.TextMessage, []byte(msg))
	_, p, err = ws.ReadMessage()
	if err != nil {
		log.Fatalf("Failed to read message: %v", err)
	}

	result = ws.ExtractResult()
	fmt.Printf("More involved example\nResponse: %s\n\nResult:\n%+v\n", p, result)
}
