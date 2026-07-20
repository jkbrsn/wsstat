package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jkbrsn/wsstat/v3"
)

// CheckStatus is the verdict of a single conformance check.
type CheckStatus int

const (
	// CheckPass means the endpoint behaved as RFC 6455 requires.
	CheckPass CheckStatus = iota
	// CheckWarn means the behavior is tolerable but deviates from the letter of the spec.
	CheckWarn
	// CheckFail means the endpoint violated a MUST-level requirement.
	CheckFail
	// CheckSkip means the check could not run (a prerequisite connection failed).
	CheckSkip
)

// String renders the verdict as the lowercase token used in JSON output.
func (s CheckStatus) String() string {
	switch s {
	case CheckPass:
		return "pass"
	case CheckWarn:
		return "warn"
	case CheckFail:
		return "fail"
	case CheckSkip:
		return "skip"
	default:
		return "unknown"
	}
}

// CheckEntry is a single check's outcome.
type CheckEntry struct {
	ID     string        // e.g. "behavior.ping-pong"
	Group  string        // "handshake" | "negotiation" | "behavior"
	Status CheckStatus   // pass/warn/fail/skip
	Detail string        // one-line detail; "" when self-evident
	Took   time.Duration // wall time spent on the check
}

// CheckReport is the outcome of a full check run. Per-status counts are derived, not stored.
type CheckReport struct {
	Target  *url.URL
	Entries []CheckEntry
}

// count returns the number of entries with the given status.
func (r *CheckReport) count(status CheckStatus) int {
	n := 0
	for _, e := range r.Entries {
		if e.Status == status {
			n++
		}
	}
	return n
}

// Passed reports the number of passing checks.
func (r *CheckReport) Passed() int { return r.count(CheckPass) }

// Warned reports the number of warning checks.
func (r *CheckReport) Warned() int { return r.count(CheckWarn) }

// Failed reports the number of failing checks.
func (r *CheckReport) Failed() int { return r.count(CheckFail) }

// Skipped reports the number of skipped checks.
func (r *CheckReport) Skipped() int { return r.count(CheckSkip) }

// Check IDs, in catalog order.
const (
	checkUpgrade         = "handshake.upgrade"
	checkAccept          = "handshake.accept"
	checkHeaders         = "handshake.headers"
	checkSubprotoNone    = "negotiation.subprotocol-none"
	checkSubprotoEcho    = "negotiation.subprotocol-echo"
	checkDeflate         = "negotiation.deflate"
	checkVersionReject   = "negotiation.version-reject"
	checkPingPong        = "behavior.ping-pong"
	checkFragmentation   = "behavior.fragmentation"
	checkCloseEcho       = "behavior.close-echo"
	checkSubprotocolName = "wsstat-check"
)

// checkOrder is the canonical order in which entries appear in the report.
var checkOrder = []string{
	checkUpgrade, checkAccept, checkHeaders,
	checkSubprotoNone, checkSubprotoEcho, checkDeflate, checkVersionReject,
	checkPingPong, checkFragmentation, checkCloseEcho,
}

const (
	// checkDefaultTimeout mirrors the core's read/dial default when --timeout is unset.
	checkDefaultTimeout = 5 * time.Second
	// checkDefaultCloseGrace mirrors the core's close-grace default when --close-timeout is
	// unset, for the run-budget arithmetic below.
	checkDefaultCloseGrace = 3 * time.Second
	// checkConnCount is the number of WebSocket connections the catalog dials; each one's
	// teardown can synchronously consume the full close grace against a non-echoing peer.
	checkConnCount = 5
	// checkRunBudgetFactor caps the whole run at this multiple of the per-check timeout
	// (plus one close grace per connection — see RunCheck) as a backstop against a wedged
	// run. The per-connection timeouts do the real bounding: the catalog performs up to nine
	// sequential operations (five dials, two pings, a fragmented write, and an HTTP probe),
	// each individually bounded by the timeout, so a slow-but-conformant endpoint responding
	// just under the timeout can legitimately need ~9x it. The budget is set above that worst
	// case so it never fails a healthy endpoint; if it does fire, the remaining checks are
	// recorded as skip, never as spurious fail.
	checkRunBudgetFactor = 10
	// RFC 6455 §7.4.1 registered close-code bounds, used to distinguish a clean 1000 echo from
	// any other valid registered code.
	minCloseCode = 1000
	maxCloseCode = 4999
	// RFC 7692 §7.1.2 permessage-deflate window-bits bounds.
	minWindowBits = 8
	maxWindowBits = 15
	// maxProbeBody bounds the version-reject probe's response read.
	maxProbeBody = 4096
)

// probeKey generates a fresh Sec-WebSocket-Key (base64 of a 16-byte random nonce, RFC 6455
// §4.1) for the version-reject probe, matching what a real dial sends so the probe is not
// trivially fingerprintable by its key.
func probeKey() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generating Sec-WebSocket-Key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(nonce[:]), nil
}

// checkTimeout returns the per-check timeout: the configured --timeout, or the default.
func (c *Client) checkTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}
	return checkDefaultTimeout
}

// checkTLSConfig returns the TLS config for the plain-HTTP version-reject probe, honoring
// --insecure. A nil config uses the transport default (verify on).
func (c *Client) checkTLSConfig() *tls.Config {
	if c.insecure {
		return &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in via --insecure
	}
	return nil
}

// checkBuilder accumulates entries by ID so the report can be assembled in canonical order
// regardless of the connection order in which checks run.
type checkBuilder struct {
	target  *url.URL
	entries map[string]CheckEntry
}

func newCheckBuilder(target *url.URL) *checkBuilder {
	return &checkBuilder{target: target, entries: make(map[string]CheckEntry, len(checkOrder))}
}

// record stores a check outcome, deriving the group from the ID prefix.
func (b *checkBuilder) record(id string, status CheckStatus, detail string, took time.Duration) {
	group := id
	if i := strings.IndexByte(id, '.'); i >= 0 {
		group = id[:i]
	}
	b.entries[id] = CheckEntry{ID: id, Group: group, Status: status, Detail: detail, Took: took}
}

// skip records the given IDs as skipped.
func (b *checkBuilder) skip(ids ...string) {
	for _, id := range ids {
		b.record(id, CheckSkip, "", 0)
	}
}

// canceled records id as skipped when the run context has ended (a Ctrl-C interrupt or the
// aggregate run budget) and reports whether it did. A dial, write, or ping that fails because
// the run was canceled reveals nothing about the endpoint's conformance, so it must not be
// scored as a warn or a MUST-level fail: doing so would let a mid-run interrupt fabricate a
// failing verdict and, via the exit-3 gate, misreport a healthy endpoint as non-conformant.
func (b *checkBuilder) canceled(ctx context.Context, id string, took time.Duration) bool {
	if ctx.Err() == nil {
		return false
	}
	detail := "run canceled"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		detail = "run budget exceeded"
	}
	b.record(id, CheckSkip, detail, took)
	return true
}

// finalize assembles the report in canonical catalog order.
func (b *checkBuilder) finalize() *CheckReport {
	r := &CheckReport{Target: b.target}
	for _, id := range checkOrder {
		if e, ok := b.entries[id]; ok {
			r.Entries = append(r.Entries, e)
		}
	}
	return r
}

// checkOptions composes the base transport options with per-connection extras. Every check
// connection discards inbound data frames: nothing here consumes payloads, and an unclaimed
// frame would block the read pump and starve pong handling (see deliverRead in wsstat.go).
func (c *Client) checkOptions(extra ...wsstat.Option) []wsstat.Option {
	opts := append(c.baseWsstatOptions(), wsstat.WithDiscardReads())
	return append(opts, extra...)
}

// RunCheck executes the Tier 1 observational RFC 6455 catalog sequentially and always returns a
// full report. A failed handshake on the first connection fails the dependent checks and skips
// the rest of the run rather than storming an unreachable endpoint. The error return is reserved
// for runtime failures the caller must surface as a non-zero exit (a malformed header); check
// verdicts live in the report.
func (c *Client) RunCheck(ctx context.Context, target *url.URL) (*CheckReport, error) {
	header, err := parseHeaders(c.headers)
	if err != nil {
		return nil, err
	}

	// The budget covers the bounded operations plus one close grace per connection: each
	// teardown synchronously waits up to the grace for the peer's close echo, which is
	// independent of the per-check timeout and would otherwise eat the budget of the
	// remaining checks against a non-echoing peer.
	to := c.checkTimeout()
	grace := c.closeGrace
	if grace <= 0 {
		grace = checkDefaultCloseGrace
	}
	runCtx, cancel := context.WithTimeout(ctx, checkRunBudgetFactor*to+checkConnCount*grace)
	defer cancel()

	b := newCheckBuilder(target)
	ok, err := c.checkHandshake(runCtx, target, header, b)
	if err != nil {
		return nil, err
	}
	if !ok {
		b.skip(checkSubprotoEcho, checkDeflate, checkVersionReject,
			checkFragmentation, checkCloseEcho)
		return b.finalize(), nil
	}
	c.checkSubprotocolEcho(runCtx, target, header, b)
	c.checkDeflateExtension(runCtx, target, header, b)
	c.checkVersionRejection(runCtx, target, header, b)
	c.checkFragmentationTolerance(runCtx, target, header, b)
	c.checkCloseHandshake(runCtx, target, header, b)
	return b.finalize(), nil
}

// checkHandshake dials the first connection and runs every check that reuses it: the handshake
// trio, the no-subprotocol-offered negotiation check, and the ping/pong behavior check. It
// reports whether the handshake succeeded; on failure the caller skips the rest of the run.
// A transport-level dial failure (DNS, connect, TLS, timeout) is returned as an error: the
// endpoint never answered, so there is nothing to score, and reporting unreachability as an
// RFC 6455 fail would let an outage or a typo trip the exit-3 CI gate.
func (c *Client) checkHandshake(
	ctx context.Context, target *url.URL, header http.Header, b *checkBuilder,
) (bool, error) {
	start := time.Now()
	ws := wsstat.New(c.checkOptions()...)
	if err := ws.DialContext(ctx, target, header); err != nil {
		ws.Close()
		if ctx.Err() != nil {
			b.skip(checkUpgrade, checkAccept, checkHeaders, checkSubprotoNone, checkPingPong)
			return false, nil
		}
		if dialTransportErr(err) {
			return false, fmt.Errorf("dialing %s: %w", target.Host, err)
		}
		b.record(checkUpgrade, CheckFail, dialDetail(err), time.Since(start))
		b.skip(checkAccept, checkHeaders, checkSubprotoNone, checkPingPong)
		return false, nil
	}
	defer ws.Close()

	b.record(checkUpgrade, CheckPass, "101 Switching Protocols", time.Since(start))
	b.record(checkAccept, CheckPass, "validated during handshake", 0)

	res := ws.ExtractResult()
	recordHeaderTokens(res, b)
	recordSubprotocolNone(res, b)
	recordPingPong(ctx, ws, b)
	return true, nil
}

// dialTransportErr reports whether a dial failure occurred before any HTTP response was
// received: DNS, TCP connect, TLS, and timeout errors all surface from net/http as a
// *url.Error, which implements net.Error, while a server that answered but refused or botched
// the upgrade yields coder's plain verification errors with no net error in the chain. The
// former is unreachability, not an RFC 6455 verdict.
func dialTransportErr(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded)
}

// recordHeaderTokens verifies the response carries the Upgrade/Connection tokens RFC 6455
// §4.2.2 requires; coder tolerates some deviation, so a miss is a warning, not a failure.
func recordHeaderTokens(res *wsstat.Result, b *checkBuilder) {
	h := res.ResponseHeaders
	switch {
	case h != nil && hasToken(h.Get("Upgrade"), "websocket") &&
		hasToken(h.Get("Connection"), "upgrade"):
		b.record(checkHeaders, CheckPass, "Upgrade/Connection tokens present", 0)
	case h == nil || !hasToken(h.Get("Upgrade"), "websocket"):
		b.record(checkHeaders, CheckWarn, `Upgrade header missing "websocket" token`, 0)
	default:
		b.record(checkHeaders, CheckWarn, `Connection header missing "Upgrade" token`, 0)
	}
}

// recordSubprotocolNone asserts the server selected no subprotocol when none was offered
// (RFC 6455 §4.2.2).
func recordSubprotocolNone(res *wsstat.Result, b *checkBuilder) {
	if res.Subprotocol == "" {
		b.record(checkSubprotoNone, CheckPass, "none offered, none selected", 0)
		return
	}
	b.record(checkSubprotoNone, CheckFail,
		fmt.Sprintf("server selected %q with none offered", res.Subprotocol), 0)
}

// recordPingPong sends a ping and expects a matching pong (RFC 6455 §5.5.2/§5.5.3).
func recordPingPong(ctx context.Context, ws *wsstat.WSStat, b *checkBuilder) {
	start := time.Now()
	err := ws.PingPongContext(ctx)
	took := time.Since(start)
	if err != nil {
		if b.canceled(ctx, checkPingPong, took) {
			return
		}
		b.record(checkPingPong, CheckFail, "no pong: "+err.Error(), took)
		return
	}
	b.record(checkPingPong, CheckPass, "ping -> pong", took)
}

// checkSubprotocolEcho offers a subprotocol and requires the server to echo one that was
// offered, or none (RFC 6455 §4.2.2). coder rejects an invented selection during the dial, and
// only that violation is the fail verdict: §4.2.2's MUST constrains the value a server may
// select when it completes the handshake, not whether it completes it. A transport failure on
// this fresh connection is a prerequisite miss (skip, as in the fragmentation and close-echo
// checks), and a refused upgrade is a warn.
func (c *Client) checkSubprotocolEcho(
	ctx context.Context, target *url.URL, header http.Header, b *checkBuilder,
) {
	if b.canceled(ctx, checkSubprotoEcho, 0) {
		return
	}
	offered := append([]string{checkSubprotocolName}, c.subprotocols...)
	start := time.Now()
	ws := wsstat.New(c.checkOptions(wsstat.WithSubprotocols(offered))...)
	if err := ws.DialContext(ctx, target, header); err != nil {
		ws.Close()
		took := time.Since(start)
		switch {
		case b.canceled(ctx, checkSubprotoEcho, took):
		case strings.Contains(err.Error(), "unexpected Sec-WebSocket-Protocol"):
			b.record(checkSubprotoEcho, CheckFail, dialDetail(err), took)
		case dialTransportErr(err):
			b.record(checkSubprotoEcho, CheckSkip, "handshake failed: "+err.Error(), took)
		default:
			b.record(checkSubprotoEcho, CheckWarn,
				"handshake rejected with subprotocol offered: "+err.Error(), took)
		}
		return
	}
	defer ws.Close()

	sel := ws.ExtractResult().Subprotocol
	took := time.Since(start)
	switch {
	case sel == "":
		b.record(checkSubprotoEcho, CheckPass, "no subprotocol selected", took)
	case slices.Contains(offered, sel):
		b.record(checkSubprotoEcho, CheckPass, "selected "+sel, took)
	default:
		b.record(checkSubprotoEcho, CheckFail, "server selected unoffered "+sel, took)
	}
}

// checkDeflateExtension negotiates permessage-deflate and validates the response parameters
// (RFC 7692 §7). A negotiation the transport rejects, or odd parameters, are warnings: the
// plain connection still works.
func (c *Client) checkDeflateExtension(
	ctx context.Context, target *url.URL, header http.Header, b *checkBuilder,
) {
	if b.canceled(ctx, checkDeflate, 0) {
		return
	}
	start := time.Now()
	ws := wsstat.New(c.checkOptions(wsstat.WithCompression(true))...)
	if err := ws.DialContext(ctx, target, header); err != nil {
		ws.Close()
		if b.canceled(ctx, checkDeflate, time.Since(start)) {
			return
		}
		b.record(checkDeflate, CheckWarn,
			"permessage-deflate negotiation failed: "+err.Error(), time.Since(start))
		return
	}
	defer ws.Close()

	ext := ws.ExtractResult().Compression
	took := time.Since(start)
	if ext == "" {
		b.record(checkDeflate, CheckPass, "permessage-deflate not negotiated", took)
		return
	}
	if detail, ok := validateDeflate(ext); ok {
		b.record(checkDeflate, CheckPass, detail, took)
	} else {
		b.record(checkDeflate, CheckWarn, detail, took)
	}
}

// checkVersionRejection probes with an unsupported Sec-WebSocket-Version and expects a non-101
// response advertising version 13 (RFC 6455 §4.4). It uses a plain HTTP request so the transport
// does not force version 13.
func (c *Client) checkVersionRejection(
	ctx context.Context, target *url.URL, header http.Header, b *checkBuilder,
) {
	if b.canceled(ctx, checkVersionReject, 0) {
		return
	}
	start := time.Now()
	status, respHeader, err := c.probeVersion(ctx, target, header)
	took := time.Since(start)
	if err != nil {
		if b.canceled(ctx, checkVersionReject, took) {
			return
		}
		b.record(checkVersionReject, CheckWarn, "version probe failed: "+err.Error(), took)
		return
	}
	// Join all header values: RFC 6455 §4.4 allows the supported versions to be advertised
	// across multiple Sec-WebSocket-Version headers.
	advertised := strings.Join(respHeader.Values("Sec-WebSocket-Version"), ", ")
	switch {
	case status == http.StatusSwitchingProtocols:
		b.record(checkVersionReject, CheckFail, "server accepted version 99 (101)", took)
	case hasToken(advertised, "13"):
		b.record(checkVersionReject, CheckPass,
			fmt.Sprintf("rejected (%d, advertises %s)", status, advertised), took)
	default:
		b.record(checkVersionReject, CheckWarn,
			fmt.Sprintf("rejected (%d) without Sec-WebSocket-Version header", status), took)
	}
}

// probeVersion sends a single upgrade request with Sec-WebSocket-Version: 99 over plain HTTP,
// honoring --insecure and custom headers, and returns the response status and headers.
func (c *Client) probeVersion(
	ctx context.Context, target *url.URL, header http.Header,
) (int, http.Header, error) {
	u := *target
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	default:
		// Leave other schemes untouched; net/http rejects them below.
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, nil, err
	}
	for name, values := range header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	key, err := probeKey()
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "99")
	req.Header.Set("Sec-WebSocket-Key", key)

	tr := c.probeTransport()
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Timeout:   c.checkTimeout(),
		Transport: tr,
		// Do not follow redirects: the verdict must be rendered against the probed URL's own
		// response, not wherever a proxy or load balancer bounces a non-upgrade request.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBody))
	}
	return resp.StatusCode, resp.Header, nil
}

// probeTransport builds the HTTP transport for the version-reject probe. It honors --insecure
// via the TLS config, proxy environment variables (as the core dial transport does — without
// this the probe would bypass a configured proxy and reach a different endpoint or none), and,
// crucially, the same --resolve host->IP overrides the WebSocket check connections apply
// through wsstat.WithResolves: without those the probe would resolve the host through the
// system resolver and target a different endpoint than every other check.
func (c *Client) probeTransport() *http.Transport {
	tr := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: c.checkTLSConfig(),
	}
	if len(c.resolves) == 0 {
		return tr
	}
	resolves := c.resolves
	dialer := &net.Dialer{Timeout: c.checkTimeout()}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, port, err := net.SplitHostPort(addr); err == nil {
			if ip, ok := resolves[net.JoinHostPort(strings.ToLower(host), port)]; ok {
				addr = net.JoinHostPort(ip, port)
			}
		}
		return dialer.DialContext(ctx, network, addr)
	}
	return tr
}

// checkFragmentationTolerance sends one text message across three frames on a connection dialed
// without compression (permessage-deflate does not preserve fragment boundaries), then pings to
// prove the connection survived (RFC 6455 §5.4). Echo content is server-dependent and unasserted.
func (c *Client) checkFragmentationTolerance(
	ctx context.Context, target *url.URL, header http.Header, b *checkBuilder,
) {
	if b.canceled(ctx, checkFragmentation, 0) {
		return
	}
	start := time.Now()
	ws := wsstat.New(c.checkOptions()...)
	if err := ws.DialContext(ctx, target, header); err != nil {
		ws.Close()
		if b.canceled(ctx, checkFragmentation, time.Since(start)) {
			return
		}
		// The dial is a prerequisite, not the behavior under test: a handshake that never
		// completed reveals nothing about fragmentation tolerance, so it is a skip (as in the
		// close-echo check), reserving fail for a server that drops the fragmented message.
		b.record(checkFragmentation, CheckSkip, "handshake failed: "+err.Error(), time.Since(start))
		return
	}
	defer ws.Close()

	fragments := [][]byte{[]byte("wsstat "), []byte("fragmentation "), []byte("check")}
	if err := ws.WriteMessageFragmented(wsstat.TextMessage, fragments); err != nil {
		if b.canceled(ctx, checkFragmentation, time.Since(start)) {
			return
		}
		b.record(checkFragmentation, CheckFail, "fragmented write failed: "+err.Error(),
			time.Since(start))
		return
	}
	if err := ws.PingPongContext(ctx); err != nil {
		if b.canceled(ctx, checkFragmentation, time.Since(start)) {
			return
		}
		b.record(checkFragmentation, CheckFail,
			"connection dropped after fragments: "+err.Error(), time.Since(start))
		return
	}
	b.record(checkFragmentation, CheckPass, "fragmented text accepted", time.Since(start))
}

// checkCloseHandshake initiates a clean close and inspects the peer's echoed status
// (RFC 6455 §5.5.1, §7.4). A 1000 echo passes; any other registered code, or no echo (TCP drop
// or timeout), warns; a dial failure skips.
func (c *Client) checkCloseHandshake(
	ctx context.Context, target *url.URL, header http.Header, b *checkBuilder,
) {
	if b.canceled(ctx, checkCloseEcho, 0) {
		return
	}
	start := time.Now()
	ws := wsstat.New(c.checkOptions()...)
	if err := ws.DialContext(ctx, target, header); err != nil {
		ws.Close()
		if b.canceled(ctx, checkCloseEcho, time.Since(start)) {
			return
		}
		b.record(checkCloseEcho, CheckSkip, "handshake failed: "+err.Error(), time.Since(start))
		return
	}
	// CloseWith blocks until teardown, so the peer's echoed status is recorded before it returns.
	if err := ws.CloseWith(minCloseCode, ""); err != nil {
		if b.canceled(ctx, checkCloseEcho, time.Since(start)) {
			return
		}
		b.record(checkCloseEcho, CheckWarn, "close failed: "+err.Error(), time.Since(start))
		return
	}
	status := ws.ReceivedCloseStatus()
	took := time.Since(start)
	switch {
	case status == minCloseCode:
		b.record(checkCloseEcho, CheckPass, "close 1000 echoed, clean shutdown", took)
	case status >= minCloseCode && status <= maxCloseCode:
		b.record(checkCloseEcho, CheckWarn,
			fmt.Sprintf("close echoed with status %d", status), took)
	default:
		b.record(checkCloseEcho, CheckWarn, "no close echo (TCP drop or timeout)", took)
	}
}

// validateDeflate checks a Sec-WebSocket-Extensions response value against RFC 7692. It returns
// a one-line detail and whether the parameters are well-formed (ok=false means warn).
func validateDeflate(ext string) (string, bool) {
	parts := strings.Split(ext, ";")
	if strings.TrimSpace(parts[0]) != "permessage-deflate" {
		return "unexpected extension: " + ext, false
	}
	seen := make(map[string]bool, len(parts))
	for _, p := range parts[1:] {
		name, val, _ := strings.Cut(strings.TrimSpace(p), "=")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if seen[name] {
			return "duplicate parameter " + name, false
		}
		seen[name] = true
		switch name {
		case "client_no_context_takeover", "server_no_context_takeover":
		case "server_max_window_bits", "client_max_window_bits":
			v := strings.TrimSpace(val)
			if v == "" {
				// Value-less window-bits is a client-request hint (RFC 7692 §7.1.2.2); in a
				// server response the parameter must carry the agreed value.
				return name + " missing value", false
			}
			if !validWindowBits(v) {
				return name + " out of range: " + v, false
			}
		default:
			return "unknown parameter " + name, false
		}
	}
	return "permessage-deflate: " + ext, true
}

// validWindowBits reports whether s is a permessage-deflate window-bits value in [8, 15].
// Extension parameter values may be quoted (RFC 6455 §9.1), so surrounding double quotes
// are stripped before parsing.
func validWindowBits(s string) bool {
	v := strings.TrimSpace(s)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return false
	}
	return n >= minWindowBits && n <= maxWindowBits
}

// hasToken reports whether a comma-separated header value contains token, case-insensitively.
func hasToken(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// dialDetail renders a one-line detail for a failed handshake.
func dialDetail(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

// validateCheck rejects the measure/stream/ping-only knobs that have no meaning in check mode.
// Check mode dials its own fixed catalog of connections and emits a structured report, so it
// takes no payloads, counts, cadences, or raw output.
func (c *Client) validateCheck() error {
	switch {
	case c.output == OutputRaw:
		return errors.New("-o raw has no meaning in check mode (no response payloads)")
	case c.responseFilePath != "":
		return errors.New("--file has no meaning in check mode (no response payloads)")
	case c.body == BodyCompact:
		return errors.New("--body has no meaning in check mode (no response payloads)")
	case c.clip:
		return errors.New("--clip has no meaning in check mode (no response payloads)")
	case len(c.textMessages) > 0:
		return errors.New("-t/--text is not supported in check mode")
	case c.rpcMethod != "":
		return errors.New("--rpc-method is not supported in check mode")
	case c.once:
		return errors.New("--once is not supported in check mode")
	case c.buffer > 0:
		return errors.New("-b/--buffer is not supported in check mode")
	case c.summaryInterval > 0:
		return errors.New("--summary-interval is not supported in check mode")
	case c.sendDelay > 0:
		return errors.New("--send-delay is not supported in check mode")
	case c.interval > 0:
		return errors.New("--interval is not supported in check mode")
	}
	return nil
}
