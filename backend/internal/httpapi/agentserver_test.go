package httpapi

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleAgentHeartbeatRejectsMissingClientCert(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest(http.MethodPost, "/agent/heartbeat", strings.NewReader(`{"peer_count":0,"load_avg":0}`))
	// No req.TLS set at all - simulates a plain (non-TLS) connection reaching the handler.
	rr := httptest.NewRecorder()
	s.handleAgentHeartbeat(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no TLS connection state, got %d", rr.Code)
	}
}

func TestHandleAgentHeartbeatRejectsEmptyPeerCertificates(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest(http.MethodPost, "/agent/heartbeat", strings.NewReader(`{"peer_count":0,"load_avg":0}`))
	req.TLS = &tls.ConnectionState{PeerCertificates: nil} // TLS present, but no client cert was presented
	rr := httptest.NewRecorder()
	s.handleAgentHeartbeat(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no peer certificate, got %d", rr.Code)
	}
}

func TestHandleAgentRegisterRejectsMissingFields(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest(http.MethodPost, "/agent/register", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	s.handleAgentRegister(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing join_token/csr_pem, got %d", rr.Code)
	}
}
