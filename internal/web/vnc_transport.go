package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/websocketsecure"
)

// handleVNCRemoteTransport bridges the embedded noVNC client's RFB WebSocket
// to the session-owned TCP connection. The session's VNC provider captured the
// target before this handler ran; no WebSocket parameter controls the upstream.
func (s *Server) handleVNCRemoteTransport(w http.ResponseWriter, r *http.Request, id string) {
	s.ctrl.Remote().AddDiagnostic(id, "noVNC browser tunnel request received")
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.ctrl.Remote().Stop(stopCtx, id)
	}()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		s.ctrl.Remote().AddDiagnostic(id, "noVNC browser tunnel upgrade failed: "+strings.TrimSpace(err.Error()))
		return
	}
	defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	secure, err := websocketsecure.ServerHandshake(ctx, ws, s.ctrl.Snapshot().Server.WebSocketPayloadMode)
	if err != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, "secure WebSocket negotiation failed")
		return
	}
	s.ctrl.Remote().AddDiagnostic(id, "noVNC browser tunnel upgraded")
	upstream, err := s.ctrl.Remote().Dial(ctx, id)
	if err != nil {
		s.ctrl.Remote().AddDiagnostic(id, "VNC target connection failed: "+strings.TrimSpace(err.Error()))
		_ = ws.Close(websocket.StatusInternalError, "VNC target connection failed")
		return
	}
	defer s.ctrl.Remote().ReleaseDial(id, upstream)
	s.ctrl.Remote().AddDiagnostic(id, "VNC target connected")

	copyDone := make(chan error, 2)
	go func() { copyDone <- copyNoVNCToTCP(ctx, secure, upstream) }()
	go func() { copyDone <- copyTCPToNoVNC(ctx, upstream, secure) }()
	err = <-copyDone
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		s.ctrl.Remote().AddDiagnostic(id, "noVNC tunnel ended: "+strings.TrimSpace(err.Error()))
	}
	cancel()
	_ = upstream.Close()
	_ = ws.Close(websocket.StatusNormalClosure, "")
	<-copyDone
}

type messageConn interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
}

func copyNoVNCToTCP(ctx context.Context, ws messageConn, upstream io.Writer) error {
	for {
		messageType, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		if messageType != websocket.MessageBinary {
			return errors.New("noVNC browser tunnel sent a non-binary message")
		}
		if _, err := upstream.Write(data); err != nil {
			return err
		}
	}
}

func copyTCPToNoVNC(ctx context.Context, upstream io.Reader, ws messageConn) error {
	buffer := make([]byte, 32*1024)
	for {
		count, err := upstream.Read(buffer)
		if count > 0 {
			if writeErr := ws.Write(ctx, websocket.MessageBinary, buffer[:count]); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			return err
		}
	}
}
