package wsstat

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CertificateDetails holds details regarding a certificate.
type CertificateDetails struct {
	CommonName         string
	Issuer             string
	NotBefore          time.Time
	NotAfter           time.Time
	PublicKeyAlgorithm x509.PublicKeyAlgorithm
	SignatureAlgorithm x509.SignatureAlgorithm
	DNSNames           []string
	IPAddresses        []net.IP
	URIs               []*url.URL
}

// Result holds durations of each phase of a WebSocket connection, cumulative durations over
// the connection timeline, and other relevant connection details.
type Result struct {
	IPs             []string             // IP addresses of the WebSocket connection
	URL             *url.URL             // URL of the WebSocket connection
	RequestHeaders  http.Header          // Headers of the initial request
	ResponseHeaders http.Header          // Headers of the response
	TLSState        *tls.ConnectionState // State of the TLS connection
	MessageCount    int                  // Number of messages sent and received

	// Subscription statistics captured when long-lived streams are active.
	Subscriptions          map[string]SubscriptionStats // Metrics by subscription ID
	SubscriptionFirstEvent time.Duration                // Time until first subscription event
	SubscriptionLastEvent  time.Duration                // Time until last subscription event

	// Duration of each phase of the connection
	DNSLookup     time.Duration // Time to resolve DNS
	TCPConnection time.Duration // TCP connection establishment time
	TLSHandshake  time.Duration // Time to perform TLS handshake
	WSHandshake   time.Duration // Time to perform WebSocket handshake
	MessageRTT    time.Duration // Time to send message and receive response

	// Cumulative durations over the connection timeline
	DNSLookupDone        time.Duration // Time to resolve DNS (might be redundant with DNSLookup)
	TCPConnected         time.Duration // Time until the TCP connection is established
	TLSHandshakeDone     time.Duration // Time until the TLS handshake is completed
	WSHandshakeDone      time.Duration // Time until the WS handshake is completed
	FirstMessageResponse time.Duration // Time until the first message is received
	TotalTime            time.Duration // Total time from opening to closing the connection
}

// Format formats the time.Duration members of Result.
func (r *Result) Format(s fmt.State, verb rune) {
	switch verb {
	case 'v':
		if s.Flag('+') {
			r.formatVerbosePlus(s)
			return
		}
		fallthrough
	default:
		r.formatCompact(s)
	}
}

// formatCompact prints the single-line comma-separated view used by %s, %q, and %v without '+'.
func (r *Result) formatCompact(s fmt.State) {
	d := r.durations()
	// Stable, readable order for single-line output
	order := []string{
		"DNSLookup", "TCPConnection", "TLSHandshake", "WSHandshake", "MessageRTT",
		"DNSLookupDone", "TCPConnected", "TLSHandshakeDone", "WSHandshakeDone",
		"FirstMessageResponse", "TotalTime",
	}
	list := make([]string, 0, len(order))
	for _, k := range order {
		v := d[k]
		if k == "TotalTime" && r.TotalTime == 0 {
			list = append(list, fmt.Sprintf("%s: - ms", k))
			continue
		}
		list = append(list, fmt.Sprintf("%s: %d ms", k, v/time.Millisecond))
	}
	io.WriteString(s, strings.Join(list, ", "))
}

// durations returns a map of the time.Duration members of Result.
func (r *Result) durations() map[string]time.Duration {
	return map[string]time.Duration{
		"DNSLookup":     r.DNSLookup,
		"TCPConnection": r.TCPConnection,
		"TLSHandshake":  r.TLSHandshake,
		"WSHandshake":   r.WSHandshake,
		"MessageRTT":    r.MessageRTT,

		"DNSLookupDone":        r.DNSLookupDone,
		"TCPConnected":         r.TCPConnected,
		"TLSHandshakeDone":     r.TLSHandshakeDone,
		"WSHandshakeDone":      r.WSHandshakeDone,
		"FirstMessageResponse": r.FirstMessageResponse,
		"TotalTime":            r.TotalTime,
	}
}

// formatVerbosePlus prints the verbose multi-line view used by %#v.
func (r *Result) formatVerbosePlus(s fmt.State) {
	r.printURLAndIPSection(s)
	r.printTLSSectionIfPresent(s)
	r.printHeadersSection(s)
	r.printDurationsSection(s)
}

// printURLAndIPSection prints the URL details and IPs, followed by a blank line.
func (r *Result) printURLAndIPSection(s fmt.State) {
	fmt.Fprintln(s, "URL")
	fmt.Fprintf(s, "  Scheme: %s\n", r.URL.Scheme)
	host, port := hostPort(r.URL)
	fmt.Fprintf(s, "  Host: %s\n", host)
	fmt.Fprintf(s, "  Port: %s\n", port)
	if r.URL.Path != "" {
		fmt.Fprintf(s, "  Path: %s\n", r.URL.Path)
	}
	if r.URL.RawQuery != "" {
		fmt.Fprintf(s, "  Query: %s\n", r.URL.RawQuery)
	}
	fmt.Fprintln(s, "IP")
	fmt.Fprintf(s, "  %v\n", r.IPs)
	fmt.Fprintln(s)
}

// printTLSSectionIfPresent prints TLS handshake details and certificate information
// if TLSState is set. It ends with a blank line only when TLS details are printed,
// matching previous behavior.
func (r *Result) printTLSSectionIfPresent(s fmt.State) {
	if r.TLSState == nil {
		return
	}
	fmt.Fprint(s, "TLS handshake details\n")
	fmt.Fprintf(s, "  Version: %s\n", tls.VersionName(r.TLSState.Version))
	fmt.Fprintf(s, "  Cipher Suite: %s\n", tls.CipherSuiteName(r.TLSState.CipherSuite))
	fmt.Fprintf(s, "  Server Name: %s\n", r.TLSState.ServerName)
	fmt.Fprintf(s, "  Handshake Complete: %t\n", r.TLSState.HandshakeComplete)

	for i, cert := range r.CertificateDetails() {
		fmt.Fprintf(s, "Certificate %d\n", i+1)
		fmt.Fprintf(s, "  Common Name: %s\n", cert.CommonName)
		fmt.Fprintf(s, "  Issuer: %s\n", cert.Issuer)
		fmt.Fprintf(s, "  Not Before: %s\n", cert.NotBefore)
		fmt.Fprintf(s, "  Not After: %s\n", cert.NotAfter)
		fmt.Fprintf(s, "  Public Key Algorithm: %s\n", cert.PublicKeyAlgorithm.String())
		fmt.Fprintf(s, "  Signature Algorithm: %s\n", cert.SignatureAlgorithm.String())
		fmt.Fprintf(s, "  DNS Names: %v\n", cert.DNSNames)
		fmt.Fprintf(s, "  IP Addresses: %v\n", cert.IPAddresses)
		fmt.Fprintf(s, "  URIs: %v\n", cert.URIs)
	}
	fmt.Fprintln(s)
}

// printHeadersSection prints request and response headers (if present) and then a blank line.
func (r *Result) printHeadersSection(s fmt.State) {
	if r.RequestHeaders != nil {
		fmt.Fprint(s, "Request headers\n")
		for k, v := range r.RequestHeaders {
			fmt.Fprintf(s, "  %s: %s\n", k, strings.Join(v, ", "))
		}
	}
	if r.ResponseHeaders != nil {
		fmt.Fprint(s, "Response headers\n")
		for k, v := range r.ResponseHeaders {
			fmt.Fprintf(s, "  %s: %s\n", k, strings.Join(v, ", "))
		}
	}
	fmt.Fprintln(s)
}

// printDurationsSection prints the durations table exactly as before.
func (r *Result) printDurationsSection(s fmt.State) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "DNS lookup:     %4d ms\n", int(r.DNSLookup/time.Millisecond))
	fmt.Fprintf(&buf, "TCP connection: %4d ms\n", int(r.TCPConnection/time.Millisecond))
	fmt.Fprintf(&buf, "TLS handshake:  %4d ms\n", int(r.TLSHandshake/time.Millisecond))
	fmt.Fprintf(&buf, "WS handshake:   %4d ms\n", int(r.WSHandshake/time.Millisecond))
	fmt.Fprintf(&buf, "Msg round trip: %4d ms\n\n", int(r.MessageRTT/time.Millisecond))

	fmt.Fprintf(&buf, "Name lookup done:   %4d ms\n", int(r.DNSLookupDone/time.Millisecond))
	fmt.Fprintf(&buf, "TCP connected:      %4d ms\n", int(r.TCPConnected/time.Millisecond))
	fmt.Fprintf(&buf, "TLS handshake done: %4d ms\n", int(r.TLSHandshakeDone/time.Millisecond))
	fmt.Fprintf(&buf, "WS handshake done:  %4d ms\n", int(r.WSHandshakeDone/time.Millisecond))
	fmt.Fprintf(&buf, "First msg response: %4d ms\n", int(r.FirstMessageResponse/time.Millisecond))

	if r.TotalTime > 0 {
		fmt.Fprintf(&buf, "Total:              %4d ms\n", int(r.TotalTime/time.Millisecond))
	} else {
		fmt.Fprintf(&buf, "Total:          %4s ms\n", "-")
	}
	io.WriteString(s, buf.String())
}

// CertificateDetails returns a slice of CertificateDetails for each certificate in the
// TLS connection.
func (r *Result) CertificateDetails() []CertificateDetails {
	if r.TLSState == nil {
		return nil
	}

	var details []CertificateDetails
	for _, cert := range r.TLSState.PeerCertificates {
		details = append(details, CertificateDetails{
			CommonName:         cert.Subject.CommonName,
			Issuer:             cert.Issuer.CommonName,
			NotBefore:          cert.NotBefore,
			NotAfter:           cert.NotAfter,
			PublicKeyAlgorithm: cert.PublicKeyAlgorithm,
			SignatureAlgorithm: cert.SignatureAlgorithm,
			DNSNames:           cert.DNSNames,
			IPAddresses:        cert.IPAddresses,
			URIs:               cert.URIs,
		})
	}

	return details
}

// hostPort returns the host and port from a URL.
func hostPort(u *url.URL) (host, port string) {
	host = u.Hostname()
	port = u.Port()
	if port == "" {
		// Return the default port based on the scheme
		switch u.Scheme {
		case "ws":
			port = "80"
		case "wss":
			port = "443"
		default:
			port = ""
		}
	}
	return host, port
}
