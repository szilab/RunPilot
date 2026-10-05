package httpgateway

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type webSocketOpen struct {
	Path      string   `json:"path"`
	Protocols []string `json:"protocols"`
}

func (g *Gateway) acceptWebSocket(f Frame) error {
	id := f.ID
	g.mu.Lock()
	socket := g.sockets[id]
	g.mu.Unlock()
	if f.Type == WebSocketOpen {
		if socket != nil || id&^WebSocketIDMask == 0 || len(f.Data) > MaxMetadata {
			return errors.New("invalid WebSocket open")
		}
		g.mu.Lock()
		channelID := id &^ WebSocketIDMask
		if channelID <= g.lastWSID {
			g.mu.Unlock()
			return errors.New("WebSocket channel IDs must increase")
		}
		g.lastWSID = channelID
		atLimit := len(g.sockets) >= MaxWebSockets
		g.mu.Unlock()
		if atLimit {
			_ = g.send(WebSocketError, id, []byte("WebSocket gateway limit reached"))
			return nil
		}
		var open webSocketOpen
		decoder := json.NewDecoder(strings.NewReader(string(f.Data)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&open) != nil || len(open.Protocols) > 16 {
			return errors.New("invalid WebSocket metadata")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return errors.New("invalid WebSocket metadata")
		}
		for _, protocol := range open.Protocols {
			if len(protocol) == 0 || len(protocol) > 128 || !validHeaderToken(protocol) {
				return errors.New("invalid WebSocket subprotocol")
			}
		}
		u, err := g.mapURL(open.Path)
		if err != nil {
			_ = g.send(WebSocketError, id, []byte("WebSocket path outside publication"))
			return nil
		}
		if u.Scheme == "http" {
			u.Scheme = "ws"
		} else {
			u.Scheme = "wss"
		}
		headers := make(http.Header)
		headers.Set("Origin", g.upstream.Scheme+"://"+g.upstream.Host)
		for key, values := range g.config.CustomHeaders {
			for _, value := range values {
				headers.Set(key, value)
			}
		}
		if g.config.ForwardPublicHost {
			headers.Set("X-Forwarded-Host", g.config.PublicHost)
		}
		if g.config.ForwardPublicScheme {
			headers.Set("X-Forwarded-Proto", g.config.PublicScheme)
		}
		if g.config.BasePathHeader != "" {
			headers.Set(g.config.BasePathHeader, g.basePathHeaderValue())
		}
		cookieURL := *u
		cookieURL.Scheme = g.upstream.Scheme
		cookies := g.jar.Cookies(&cookieURL)
		if len(cookies) != 0 {
			values := make([]string, 0, len(cookies))
			for _, cookie := range cookies {
				values = append(values, cookie.String())
			}
			headers.Set("Cookie", strings.Join(values, "; "))
		}
		ctx, cancel := context.WithTimeout(g.ctx, 30*time.Second)
		conn, response, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: headers, Subprotocols: open.Protocols, CompressionMode: websocket.CompressionDisabled})
		cancel()
		if response != nil {
			g.jar.SetCookies(&cookieURL, response.Cookies())
		}
		if err != nil {
			_ = g.send(WebSocketError, id, []byte("upstream WebSocket connection failed"))
			return nil
		}
		conn.SetReadLimit(MaxWebSocketMessage)
		socket = &upstreamSocket{conn: conn, write: make(chan wsMessage, 4), wake: make(chan struct{}, 1)}
		g.mu.Lock()
		if len(g.sockets) >= MaxWebSockets || g.sockets[id] != nil {
			g.mu.Unlock()
			_ = conn.CloseNow()
			return errors.New("WebSocket limit or duplicate ID")
		}
		g.sockets[id] = socket
		g.mu.Unlock()
		meta, _ := json.Marshal(map[string]string{"protocol": conn.Subprotocol()})
		if err := g.send(WebSocketOpened, id, meta); err != nil {
			g.removeSocket(id, socket)
			return err
		}
		g.wg.Add(2)
		go g.upstreamWebSocketRead(id, socket)
		go g.upstreamWebSocketWrite(id, socket)
		return nil
	}
	if socket == nil {
		if f.Type == WebSocketClose {
			return nil
		}
		return errors.New("unknown WebSocket channel")
	}
	switch f.Type {
	case WebSocketText, WebSocketBinary:
		if len(f.Data) < 2 || len(f.Data) > Chunk {
			return errors.New("invalid WebSocket chunk")
		}
		kind := websocket.MessageBinary
		if f.Data[0] == byte(websocket.MessageText) {
			kind = websocket.MessageText
		} else if f.Data[0] != byte(websocket.MessageBinary) {
			return errors.New("invalid WebSocket message type")
		}
		if f.Data[1] > 1 {
			return errors.New("invalid WebSocket chunk flags")
		}
		if len(socket.message) == 0 {
			socket.messageType = kind
		} else if socket.messageType != kind {
			return errors.New("mixed WebSocket message types")
		}
		if len(socket.message)+len(f.Data)-2 > MaxWebSocketMessage {
			socket.clearMessage()
			_ = g.send(WebSocketError, id, []byte("WebSocket message exceeds limit"))
			g.removeSocket(id, socket)
			return nil
		}
		socket.message = append(socket.message, f.Data[2:]...)
		if f.Data[1] == 1 {
			message := wsMessage{kind: socket.messageType, data: append([]byte(nil), socket.message...)}
			socket.clearMessage()
			socket.mu.Lock()
			socket.queued += len(message.data)
			queued := socket.queued
			socket.mu.Unlock()
			if queued > MaxWebSocketMessage {
				socket.mu.Lock()
				socket.queued -= len(message.data)
				socket.mu.Unlock()
				_ = g.send(WebSocketError, id, []byte("WebSocket send queue exceeded"))
				g.removeSocket(id, socket)
				return nil
			}
			select {
			case socket.write <- message:
			default:
				socket.mu.Lock()
				socket.queued -= len(message.data)
				socket.mu.Unlock()
				_ = g.send(WebSocketError, id, []byte("WebSocket send queue exceeded"))
				g.removeSocket(id, socket)
			}
		}
	case WebSocketCredit:
		if len(f.Data) != 4 {
			return errors.New("invalid WebSocket credit")
		}
		n := int(binary.BigEndian.Uint32(f.Data))
		socket.mu.Lock()
		if n < 1 || n > WebSocketWindow || socket.credit+n > WebSocketWindow {
			socket.mu.Unlock()
			return errors.New("WebSocket credit exceeded")
		}
		socket.credit += n
		socket.mu.Unlock()
		select {
		case socket.wake <- struct{}{}:
		default:
		}
	case WebSocketClose:
		var closeInfo struct {
			Code   int    `json:"code"`
			Reason string `json:"reason"`
		}
		if len(f.Data) > 128 || json.Unmarshal(f.Data, &closeInfo) != nil || (closeInfo.Code != 0 && closeInfo.Code != 1000 && (closeInfo.Code < 3000 || closeInfo.Code > 4999)) || len(closeInfo.Reason) > 123 {
			return errors.New("invalid WebSocket close")
		}
		if closeInfo.Code == 0 {
			closeInfo.Code = int(websocket.StatusNormalClosure)
		}
		_ = socket.conn.Close(websocket.StatusCode(closeInfo.Code), closeInfo.Reason)
		time.AfterFunc(5*time.Second, func() { g.removeSocket(id, socket) })
	default:
		return errors.New("unknown WebSocket tunnel frame")
	}
	return nil
}

func (s *upstreamSocket) clearMessage() { s.message = nil }

func (g *Gateway) removeSocket(id uint32, socket *upstreamSocket) {
	g.mu.Lock()
	if g.sockets[id] == socket {
		delete(g.sockets, id)
	}
	g.mu.Unlock()
	_ = socket.conn.CloseNow()
}

func (g *Gateway) upstreamWebSocketWrite(id uint32, socket *upstreamSocket) {
	defer g.wg.Done()
	defer g.removeSocket(id, socket)
	for {
		select {
		case <-g.ctx.Done():
			return
		case message := <-socket.write:
			ctx, cancel := context.WithTimeout(g.ctx, 30*time.Second)
			err := socket.conn.Write(ctx, message.kind, message.data)
			cancel()
			socket.mu.Lock()
			socket.queued -= len(message.data)
			socket.mu.Unlock()
			if err != nil {
				_ = g.send(WebSocketError, id, []byte("upstream WebSocket write failed"))
				return
			}
		}
	}
}

func (g *Gateway) upstreamWebSocketRead(id uint32, socket *upstreamSocket) {
	defer g.wg.Done()
	defer g.removeSocket(id, socket)
	for {
		ctx, cancel := context.WithTimeout(g.ctx, 5*time.Minute)
		kind, message, err := socket.conn.Read(ctx)
		cancel()
		if err != nil {
			code := websocket.CloseStatus(err)
			if code == -1 {
				code = websocket.StatusAbnormalClosure
			}
			reason := ""
			wasClean := false
			var closeErr websocket.CloseError
			if errors.As(err, &closeErr) {
				reason = closeErr.Reason
				wasClean = true
			}
			payload, _ := json.Marshal(map[string]any{"code": code, "reason": reason, "wasClean": wasClean})
			_ = g.send(WebSocketClosed, id, payload)
			return
		}
		if len(message) > MaxWebSocketMessage {
			_ = g.send(WebSocketError, id, []byte("upstream WebSocket message exceeds limit"))
			return
		}
		frameType := WebSocketBinary
		if kind == websocket.MessageText {
			frameType = WebSocketText
		}
		if len(message) == 0 {
			if g.send(frameType, id, []byte{byte(kind), 1}) != nil {
				return
			}
			continue
		}
		for offset := 0; offset < len(message); {
			amount := len(message) - offset
			if amount > Chunk-2 {
				amount = Chunk - 2
			}
			for {
				socket.mu.Lock()
				credit := socket.credit
				if credit > amount {
					credit = amount
				}
				if credit > 0 {
					socket.credit -= credit
				}
				socket.mu.Unlock()
				if credit > 0 {
					amount = credit
					break
				}
				select {
				case <-g.ctx.Done():
					return
				case <-socket.wake:
				}
			}
			last := byte(0)
			if offset+amount == len(message) {
				last = 1
			}
			data := make([]byte, amount+2)
			data[0], data[1] = byte(kind), last
			copy(data[2:], message[offset:offset+amount])
			if g.send(frameType, id, data) != nil {
				return
			}
			offset += amount
		}
	}
}
