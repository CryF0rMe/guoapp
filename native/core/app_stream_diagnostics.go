package core

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

var nativeStreamRequestID atomic.Uint64

type nativeStreamDiagnosticKey struct{}

type nativeStreamDiagnostic struct {
	request uint64
	session string
	asset   string
	method  string
	started time.Time
}

func nativeStreamID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:6])
}

func nativeStreamTarget(address string) (string, string) {
	parsed, err := url.Parse(address)
	if err != nil {
		return "", "invalid"
	}
	parts := strings.Split(parsed.EscapedPath(), "/")
	for i, part := range parts {
		if len(part) > 48 {
			parts[i] = "[redacted-" + nativeStreamID(part) + "]"
		}
	}
	return parsed.Hostname(), truncate(strings.Join(parts, "/"), 512)
}

func nativeStreamError(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return "tls"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return "connect"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected_eof"
	}
	return "io_error"
}

func (stream *nativeStreamServer) nativeLog(info nativeStreamDiagnostic, event, address, detail string, status int, contentType string) {
	host, target := nativeStreamTarget(address)
	mediaType, _, _ := mime.ParseMediaType(contentType)
	stream.downloader.recordDiagnostic(diagnosticEvent{
		Event: "stream." + event, Level: "info", Host: host, HTTPStatus: status, ResponseType: mediaType,
		Message: fmt.Sprintf("request=%d session=%s asset=%s method=%s path=%s elapsed_ms=%d %s",
			info.request, info.session, info.asset, info.method, target, time.Since(info.started).Milliseconds(), detail),
	})
}

func nativePlaylistKind(body string) string {
	if strings.Contains(body, "#EXT-X-STREAM-INF:") || strings.Contains(body, "#EXT-X-I-FRAME-STREAM-INF:") {
		return "master_playlist"
	}
	return "media_playlist"
}

type nativeDiagnosticBody struct {
	io.ReadCloser
	stream  *nativeStreamServer
	info    nativeStreamDiagnostic
	address string
}

func (body *nativeDiagnosticBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if err != nil && err != io.EOF {
		body.stream.nativeLog(body.info, "upstream_read_error", body.address, "error="+nativeStreamError(err), 0, "")
	}
	return n, err
}

type nativeDiagnosticWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	err    error
}

func (writer *nativeDiagnosticWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *nativeDiagnosticWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	n, err := writer.ResponseWriter.Write(data)
	writer.bytes += int64(n)
	if err != nil {
		writer.err = err
	}
	return n, err
}

func (stream *nativeStreamServer) nativeTrace(info nativeStreamDiagnostic, address string) *httptrace.ClientTrace {
	phase := func(name string, err error) {
		stream.nativeLog(info, "network_phase", address, "phase="+name+" error="+nativeStreamError(err), 0, "")
	}
	return &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { phase("dns_start", nil) },
		DNSDone:           func(value httptrace.DNSDoneInfo) { phase("dns_done", value.Err) },
		ConnectStart:      func(string, string) { phase("connect_start", nil) },
		ConnectDone:       func(_, _ string, err error) { phase("connect_done", err) },
		TLSHandshakeStart: func() { phase("tls_start", nil) },
		TLSHandshakeDone:  func(_ tls.ConnectionState, err error) { phase("tls_done", err) },
		GotConn: func(value httptrace.GotConnInfo) {
			stream.nativeLog(info, "network_phase", address, fmt.Sprintf("phase=got_connection reused=%t", value.Reused), 0, "")
		},
		GotFirstResponseByte: func() { phase("first_response_byte", nil) },
	}
}
