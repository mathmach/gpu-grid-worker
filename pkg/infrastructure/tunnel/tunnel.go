package tunnel

import (
	"context"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mathmach/gpu-grid-worker/pkg/wsconn"
)

type SafeWSConn = wsconn.Conn

func NewSafeWSConn(conn *websocket.Conn) *SafeWSConn {
	return wsconn.New(conn)
}

type WSClientDialer struct{}

func NewWSClientDialer() *WSClientDialer {
	return &WSClientDialer{}
}

func (d *WSClientDialer) DialWorkerSocket(ctx context.Context, wsURL string) (*SafeWSConn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   64 * 1024,
		WriteBufferSize:  64 * 1024,
	}
	rawWS, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	rawWS.SetReadLimit(64 * 1024 * 1024)
	return NewSafeWSConn(rawWS), nil
}
