package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/remote"
)

const maxRDCleanPathPDU = 64 * 1024

func (s *Server) handleRemoteTransportTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	client, err := s.ctrl.Remote().Client(id)
	if err != nil {
		remoteError(w, err)
		return
	}
	if client.Kind != remote.ClientIronRDP {
		writeError(w, http.StatusConflict, errors.New("remote session does not use a transport client"))
		return
	}
	ticket, err := secureTicket()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.rdpTicketMu.Lock()
	s.rdpTickets[ticket] = remoteTransportTicket{SessionID: id, Expires: time.Now().Add(time.Minute)}
	s.rdpTicketMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"ticket": ticket})
}

// handleRemoteTransport deliberately takes no target address from its URL or
// WebSocket query. IronRDP sends the ticket inside its RDCleanPath handshake;
// the parsed destination field is ignored and the provider snapshot controls
// every outbound TCP dial.
func (s *Server) handleRemoteTransport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	kind, request, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageBinary {
		_ = conn.Close(websocket.StatusPolicyViolation, "RDCleanPath handshake required")
		return
	}
	handshake, err := parseRDCleanPathRequest(request)
	if err != nil || !s.consumeRDPTransportTicket(handshake.proxyAuth, id) {
		_ = conn.Close(websocket.StatusPolicyViolation, "RDP transport ticket expired or invalid")
		return
	}

	upstream, err := s.ctrl.Remote().Dial(ctx, id)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "RDP target connection failed")
		return
	}
	defer s.ctrl.Remote().ReleaseDial(id, upstream)
	// RDP has no child process to outlive its browser client. Once an attached
	// transport ends, release the logical Remote session as well.
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = s.ctrl.Remote().Stop(stopCtx, id)
	}()
	if _, err := upstream.Write(handshake.x224); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "RDP negotiation failed")
		return
	}
	x224, err := readX224Response(upstream)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "RDP negotiation failed")
		return
	}
	session, err := s.ctrl.Remote().Get(id)
	if err != nil || session.RDP == nil {
		_ = conn.Close(websocket.StatusInternalError, "RDP target snapshot unavailable")
		return
	}
	// RDCleanPath requires the proxy to terminate target TLS and sends the
	// target certificate chain to IronRDP for its protocol-level verification.
	// RDP servers commonly use self-signed certificates, so Go cannot apply
	// Web-PKI validation here. IronRDP receives the target public key through
	// this authenticated one-time handshake for CredSSP verification.
	tlsConn := tls.Client(upstream, &tls.Config{ServerName: session.RDP.Host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- see RDCleanPath rationale above.
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "RDP TLS negotiation failed")
		return
	}
	certificates := tlsConn.ConnectionState().PeerCertificates
	if len(certificates) == 0 {
		_ = conn.Close(websocket.StatusInternalError, "RDP target did not present a certificate")
		return
	}
	response, err := encodeRDCleanPathResponse(x224, certificates, upstream.RemoteAddr())
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "RDP transport initialization failed")
		return
	}
	if err := conn.Write(ctx, websocket.MessageBinary, response); err != nil {
		return
	}

	wsConn := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(tlsConn, wsConn)
		close(done)
	}()
	_, _ = io.Copy(wsConn, tlsConn)
	cancel()
	_ = wsConn.Close()
	_ = tlsConn.Close()
	<-done
}

func (s *Server) consumeRDPTransportTicket(value, id string) bool {
	s.rdpTicketMu.Lock()
	defer s.rdpTicketMu.Unlock()
	ticket, ok := s.rdpTickets[value]
	if ok {
		delete(s.rdpTickets, value)
	}
	return ok && ticket.SessionID == id && time.Now().Before(ticket.Expires)
}

type rdcRequest struct {
	proxyAuth string
	x224      []byte
}

func parseRDCleanPathRequest(data []byte) (rdcRequest, error) {
	tag, body, rest, err := derTLV(data)
	if err != nil || tag != 0x30 || len(rest) != 0 {
		return rdcRequest{}, errors.New("invalid RDCleanPath envelope")
	}
	var out rdcRequest
	for len(body) > 0 {
		tag, value, next, err := derTLV(body)
		if err != nil {
			return rdcRequest{}, err
		}
		body = next
		switch tag {
		case 0xa3:
			_, text, _, err := derTLV(value)
			if err != nil {
				return rdcRequest{}, errors.New("invalid RDCleanPath authorization")
			}
			out.proxyAuth = string(text)
		case 0xa6:
			innerTag, bytes, _, err := derTLV(value)
			if err != nil || innerTag != 0x04 {
				return rdcRequest{}, errors.New("invalid RDCleanPath X.224 request")
			}
			out.x224 = append([]byte(nil), bytes...)
		}
	}
	if out.proxyAuth == "" || len(out.x224) == 0 {
		return rdcRequest{}, errors.New("incomplete RDCleanPath request")
	}
	return out, nil
}

func encodeRDCleanPathResponse(x224 []byte, certificates []*x509.Certificate, address net.Addr) ([]byte, error) {
	fields := append(derExplicit(0, derInteger(3390)), derExplicit(6, derOctets(x224))...)
	certs := make([]byte, 0)
	for _, certificate := range certificates {
		certs = append(certs, derOctets(certificate.Raw)...)
	}
	fields = append(fields, derExplicit(7, derSequence(certs))...)
	fields = append(fields, derExplicit(9, derUTF8(address.String()))...)
	return derSequence(fields), nil
}

func readX224Response(conn net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	if header[0] != 3 || header[1] != 0 {
		return nil, errors.New("invalid RDP TPKT response")
	}
	length := int(header[2])<<8 | int(header[3])
	if length < 7 || length > 512 {
		return nil, fmt.Errorf("invalid RDP TPKT response length %d", length)
	}
	response := make([]byte, length)
	copy(response, header)
	_, err := io.ReadFull(conn, response[4:])
	return response, err
}

func derTLV(data []byte) (byte, []byte, []byte, error) {
	if len(data) < 2 || len(data) > maxRDCleanPathPDU {
		return 0, nil, nil, errors.New("invalid DER value")
	}
	length := int(data[1])
	offset := 2
	if length&0x80 != 0 {
		count := length & 0x7f
		if count == 0 || count > 3 || len(data) < offset+count {
			return 0, nil, nil, errors.New("invalid DER length")
		}
		length = 0
		for _, b := range data[offset : offset+count] {
			length = length<<8 | int(b)
		}
		offset += count
	}
	if length < 0 || length > maxRDCleanPathPDU || len(data) < offset+length {
		return 0, nil, nil, errors.New("truncated DER value")
	}
	return data[0], data[offset : offset+length], data[offset+length:], nil
}

func derLength(length int) []byte {
	if length < 128 {
		return []byte{byte(length)}
	}
	if length <= 0xff {
		return []byte{0x81, byte(length)}
	}
	return []byte{0x82, byte(length >> 8), byte(length)}
}
func der(tag byte, value []byte) []byte {
	out := []byte{tag}
	out = append(out, derLength(len(value))...)
	return append(out, value...)
}
func derSequence(value []byte) []byte           { return der(0x30, value) }
func derOctets(value []byte) []byte             { return der(0x04, value) }
func derUTF8(value string) []byte               { return der(0x0c, []byte(value)) }
func derExplicit(tag byte, value []byte) []byte { return der(0xa0|tag, value) }
func derInteger(value int) []byte {
	if value <= 0xff {
		return der(0x02, []byte{byte(value)})
	}
	return der(0x02, []byte{byte(value >> 8), byte(value)})
}
