// Mock WebSocket server for the wsstat dev stack. Each URL path maps to one
// deterministic behavior so a single wsstat CLI feature can be exercised in
// isolation against a live peer. No TLS in this iteration (see dev/README.md).
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// largeResponseBytes sizes the /large reply past coder's 32 KiB default read
// limit, exercising the client's unbounded-read path on a real frame.
const largeResponseBytes = 1 << 20 // 1 MiB

// slowDelay is how long /slow stalls before replying, long enough to trip a
// short client -timeout.
const slowDelay = 3 * time.Second

type behavior func(*http.Request, *websocket.Conn)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", handle(echoBehavior))
	mux.HandleFunc("/jsonrpc", handle(jsonrpcBehavior))
	mux.HandleFunc("/stream", handle(streamBehavior))
	mux.HandleFunc("/large", handle(largeBehavior))
	mux.HandleFunc("/slow", handle(slowBehavior))
	mux.HandleFunc("/headers", handle(headersBehavior))
	mux.HandleFunc("/close-abrupt", handle(closeAbruptBehavior))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	log.Println("mock-ws listening on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatal(err)
	}
}

// handle wraps a behavior with Accept + an unbounded read limit (so /large works
// regardless of the client side), then hands over the connection. The behavior
// owns the connection lifecycle and closes it.
func handle(b behavior) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			log.Println("accept failed:", err)
			return
		}
		conn.SetReadLimit(-1)
		b(r, conn)
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
}

// echoBehavior echoes every frame back unchanged, preserving the message type.
// Serves -text, -count bursts, default ping/pong, -resolve, -f, and verbosity.
func echoBehavior(_ *http.Request, conn *websocket.Conn) {
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx := context.Background()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if err := conn.Write(ctx, typ, data); err != nil {
			return
		}
	}
}

// jsonrpcBehavior replies with a JSON-RPC result echoing the request id, or a
// -32700 parse error on malformed input. Serves -rpc-method.
func jsonrpcBehavior(_ *http.Request, conn *websocket.Conn) {
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx := context.Background()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var req rpcRequest
		var out []byte
		if err := json.Unmarshal(data, &req); err != nil {
			out = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`)
		} else {
			out, _ = json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      rawOrNull(req.ID),
				"result":  "ok",
			})
		}
		if err := conn.Write(ctx, websocket.MessageText, out); err != nil {
			return
		}
	}
}

// largeBehavior replies with a single valid JSON-RPC frame whose result exceeds
// 32 KiB, so the only thing under test is frame size, not parsing.
func largeBehavior(_ *http.Request, conn *websocket.Conn) {
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx := context.Background()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var req rpcRequest
		_ = json.Unmarshal(data, &req)
		out, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      rawOrNull(req.ID),
			"result":  strings.Repeat("a", largeResponseBytes),
		})
		if err := conn.Write(ctx, websocket.MessageText, out); err != nil {
			return
		}
	}
}

// slowBehavior stalls past a short client timeout before echoing. Serves the
// -timeout failure path.
func slowBehavior(_ *http.Request, conn *websocket.Conn) {
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx := context.Background()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		time.Sleep(slowDelay)
		if err := conn.Write(ctx, typ, data); err != nil {
			return
		}
	}
}

// headersBehavior replies with the value of the X-Smoke request header, so the
// test can assert -H/-header was transmitted on the upgrade request.
func headersBehavior(r *http.Request, conn *websocket.Conn) {
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx := context.Background()
	reflected := r.Header.Get("X-Smoke")
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
		if err := conn.Write(ctx, websocket.MessageText, []byte(reflected)); err != nil {
			return
		}
	}
}

// closeAbruptBehavior reads one frame, then drops the connection with no close
// frame (CloseNow). Tests client error handling and the close-handshake path.
func closeAbruptBehavior(_ *http.Request, conn *websocket.Conn) {
	ctx := context.Background()
	if _, _, err := conn.Read(ctx); err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	conn.CloseNow()
}

// streamBehavior waits for an initial subscribe frame, then pumps JSON
// notifications until the client disconnects or an optional ?count is reached.
// ?rate sets notifications per second (default 1). Serves -subscribe,
// -subscribe-once, -summary-interval, and -buffer.
func streamBehavior(r *http.Request, conn *websocket.Conn) {
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Realistic subscription model: stream only starts after the client's first
	// frame. wsstat sends nothing for a bare -subscribe (empty payload), so the
	// smoke cases always pass an initial -t payload.
	if _, _, err := conn.Read(ctx); err != nil {
		return
	}

	rate := queryInt(r, "rate", 1)
	if rate < 1 {
		rate = 1
	}
	maxCount := queryInt(r, "count", 0) // 0 = unbounded

	// A client disconnect (or close frame) cancels the pump.
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()

	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	var n int
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n++
			notif, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  "subscription",
				"params":  map[string]any{"subscription": "sub1", "result": n},
			})
			if err := conn.Write(ctx, websocket.MessageText, notif); err != nil {
				return
			}
			if maxCount > 0 && n >= maxCount {
				return
			}
		}
	}
}

// rawOrNull returns the raw JSON value, or a JSON null when it is empty.
func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// queryInt reads an integer query parameter, falling back to def when absent or
// unparseable.
func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
