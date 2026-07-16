package wsstat

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloseStatus checks the standalone CloseStatus helper on non-close errors.
func TestCloseStatus(t *testing.T) {
	assert.Equal(t, -1, CloseStatus(nil))
	assert.Equal(t, -1, CloseStatus(context.Canceled))
}

// TestReceivedCloseStatus dials the shared echo server, initiates a client close with code
// 1000 while a reader drains, and asserts the peer's echoed close status is captured. The
// drained read error carrying the status loses its delivery race with teardown (settling the
// close-echo race the plan flags), so the reliable route is ReceivedCloseStatus, populated in
// the read pump.
func TestReceivedCloseStatus(t *testing.T) {
	ws := New(WithTimeout(2 * time.Second))
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))
	assert.Equal(t, -1, ws.ReceivedCloseStatus(), "no close frame read yet")

	go func() {
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}()

	require.NoError(t, ws.CloseWith(1000, ""))
	assert.Equal(t, 1000, ws.ReceivedCloseStatus(),
		"peer's echoed close status should be captured by the read pump")
}

// TestWriteMessageFragmented sends a 3-fragment text message to the shared echo server and
// reads back a single reassembled echo.
func TestWriteMessageFragmented(t *testing.T) {
	ws := New(WithTimeout(2 * time.Second))
	require.NoError(t, ws.DialContext(context.Background(), echoServerAddrWs, http.Header{}))
	defer ws.Close()

	fragments := [][]byte{[]byte("frag-one|"), []byte("frag-two|"), []byte("frag-three")}
	require.NoError(t, ws.WriteMessageFragmented(TextMessage, fragments))

	mt, data, err := ws.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, TextMessage, mt)
	assert.Equal(t, "frag-one|frag-two|frag-three", string(data))
}

// rawFrame is a decoded RFC 6455 frame captured off the wire.
type rawFrame struct {
	fin     bool
	opcode  byte
	payload []byte
}

// TestWriteMessageFragmentedFraming verifies per-Write framing: coder's echo server
// reassembles fragments transparently, so this uses a hand-rolled server that reads raw
// frames off the socket and asserts one non-final frame per fragment (FIN set only on the
// trailing frame coder emits from Writer.Close).
func TestWriteMessageFragmentedFraming(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	frames := make(chan []rawFrame, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		br := bufio.NewReader(conn)
		if err := rawHandshake(br, conn); err != nil {
			return
		}
		var captured []rawFrame
		for {
			f, err := readRawFrame(br)
			if err != nil {
				return
			}
			captured = append(captured, f)
			if f.fin {
				frames <- captured
				return
			}
		}
	}()

	host := ln.Addr().String()
	target := &url.URL{Scheme: "ws", Host: host}
	ws := New(WithTimeout(2 * time.Second))
	require.NoError(t, ws.DialContext(context.Background(), target, http.Header{}))
	defer ws.Close()

	fragments := [][]byte{[]byte("aa"), []byte("bbb"), []byte("cccc")}
	require.NoError(t, ws.WriteMessageFragmented(TextMessage, fragments))

	select {
	case captured := <-frames:
		// coder emits one non-final frame per fragment, then a trailing empty FIN frame
		// from Writer.Close: 4 frames total, FIN only on the last.
		require.Len(t, captured, len(fragments)+1)
		assert.Equal(t, byte(0x1), captured[0].opcode, "first frame is a text frame")
		assert.Equal(t, []byte("aa"), captured[0].payload)
		assert.Equal(t, []byte("bbb"), captured[1].payload)
		assert.Equal(t, []byte("cccc"), captured[2].payload)
		for i, f := range captured {
			last := i == len(captured)-1
			assert.Equal(t, last, f.fin, "frame %d FIN", i)
			if i > 0 {
				assert.Equal(t, byte(0x0), f.opcode, "continuation opcode on frame %d", i)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for raw frames")
	}
}

// rawHandshake reads the client's upgrade request and writes a minimal 101 response,
// completing the RFC 6455 opening handshake so raw frames can be read afterward.
func rawHandshake(br *bufio.Reader, w io.Writer) error {
	req, err := http.ReadRequest(br)
	if err != nil {
		return err
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(h[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	_, err = io.WriteString(w, resp)
	return err
}

// readRawFrame decodes one masked client frame (RFC 6455 §5.2). Payload lengths in these
// tests stay under 126 bytes, so the extended-length forms are not handled.
func readRawFrame(br *bufio.Reader) (rawFrame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return rawFrame{}, err
	}
	fin := hdr[0]&0x80 != 0
	opcode := hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	length := int(hdr[1] & 0x7f)
	if length >= 126 {
		var ext [8]byte
		if length == 126 {
			if _, err := io.ReadFull(br, ext[:2]); err != nil {
				return rawFrame{}, err
			}
			length = int(binary.BigEndian.Uint16(ext[:2]))
		} else {
			if _, err := io.ReadFull(br, ext[:8]); err != nil {
				return rawFrame{}, err
			}
			length = int(binary.BigEndian.Uint64(ext[:8]))
		}
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(br, mask[:]); err != nil {
			return rawFrame{}, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(br, payload); err != nil {
		return rawFrame{}, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return rawFrame{fin: fin, opcode: opcode, payload: payload}, nil
}
