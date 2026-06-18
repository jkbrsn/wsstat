// Mock WebSocket server for the wsstat dev stack. Each URL path maps to one
// deterministic behavior so a single wsstat CLI feature can be exercised in
// isolation against a live peer. Serves both ws:// (plain) and wss:// (TLS with
// a startup-generated self-signed cert); see dev/README.md.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
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
	cert, caPEM, err := selfSignedCert()
	if err != nil {
		log.Fatal("generate cert:", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/echo", handle(echoBehavior))
	mux.HandleFunc("/jsonrpc", handle(jsonrpcBehavior))
	mux.HandleFunc("/stream", handle(streamBehavior))
	mux.HandleFunc("/large", handle(largeBehavior))
	mux.HandleFunc("/slow", handle(slowBehavior))
	mux.HandleFunc("/headers", handle(headersBehavior))
	mux.HandleFunc("/close-abrupt", handle(closeAbruptBehavior))
	mux.HandleFunc("/push", handle(pushBehavior))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// /ca.pem publishes the server's self-signed cert so a verifying client can
	// trust it via SSL_CERT_FILE without -insecure. Public material, safe to serve.
	mux.HandleFunc("/ca.pem", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(caPEM)
	})

	plainAddr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		plainAddr = ":" + p
	}
	tlsAddr := ":8443"
	if p := os.Getenv("TLS_PORT"); p != "" {
		tlsAddr = ":" + p
	}

	// Bind both ports before serving so a passing /healthz on the plain port
	// implies the TLS port is already accepting connections.
	plainLn, err := net.Listen("tcp", plainAddr)
	if err != nil {
		log.Fatal("listen plain:", err)
	}
	tlsLn, err := net.Listen("tcp", tlsAddr)
	if err != nil {
		log.Fatal("listen tls:", err)
	}

	tlsSrv := &http.Server{Handler: mux, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	go func() {
		log.Println("mock-ws (wss) listening on", tlsAddr)
		if err := tlsSrv.ServeTLS(tlsLn, "", ""); err != nil {
			log.Fatal("serve tls:", err)
		}
	}()

	log.Println("mock-ws (ws) listening on", plainAddr)
	if err := http.Serve(plainLn, mux); err != nil {
		log.Fatal("serve plain:", err)
	}
}

// selfSignedCert generates an in-memory ECDSA self-signed certificate valid for
// the dev-stack dial targets, returning the tls.Certificate and its PEM-encoded
// public cert (served at /ca.pem so a verifying client can trust it).
func selfSignedCert() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "wsstat-dev-mock"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost", "mock"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return cert, certPEM, nil
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
// Serves --text, --count bursts, default ping/pong, --resolve, -o/--body/--clip, and verbosity.
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

// pushBehavior is a write-only / non-echoing peer: it pumps JSON notifications
// and never reads, so it never processes the client's Close frame and never
// sends the closing-handshake echo. This exercises the client-side close path,
// where coder's Conn.Close blocks up to 5s waiting for an echo that never
// arrives, so a smoke case can observe and bound teardown latency. Models
// event-pushing servers that ignore inbound frames. ?rate sets msgs/sec.
func pushBehavior(r *http.Request, conn *websocket.Conn) {
	// No graceful close: this peer never reads the echo, so CloseNow is honest.
	defer conn.CloseNow()
	ctx := context.Background()

	rate := queryInt(r, "rate", 10)
	if rate < 1 {
		rate = 1
	}
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	var n int
	for range ticker.C {
		n++
		notif, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "subscription",
			"params":  map[string]any{"subscription": "push", "result": n},
		})
		// Write fails once the client's TCP teardown completes; that ends the loop.
		if err := conn.Write(ctx, websocket.MessageText, notif); err != nil {
			return
		}
	}
}

// streamBehavior waits for an initial subscribe frame, then pumps JSON
// notifications until the client disconnects or an optional ?count is reached.
// ?rate sets notifications per second (default 1). Serves the stream
// subcommand, --once, --summary-interval, and --buffer.
func streamBehavior(r *http.Request, conn *websocket.Conn) {
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Realistic subscription model: stream only starts after the client's first
	// frame. wsstat sends nothing for a bare `stream` (empty payload), so the
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
