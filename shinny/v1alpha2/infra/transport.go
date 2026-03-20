package infra

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

type WSConn struct {
	conn *websocket.Conn
}

func DialWS(ctx context.Context, url string, header http.Header) (*WSConn, *http.Response, error) {
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		return nil, resp, err
	}
	// Market snapshots can exceed the library default (64 MiB) for large view_width.
	c.SetReadLimit(64 << 20)
	return &WSConn{conn: c}, resp, nil
}

func (c *WSConn) ReadJSON(ctx context.Context, out any) error {
	_, payload, err := c.conn.Read(ctx)
	if err != nil {
		return err
	}
	return DecodeJSON(payload, out)
}

func (c *WSConn) WriteJSON(ctx context.Context, v any) error {
	payload, err := EncodeJSON(v)
	if err != nil {
		return err
	}
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

func (c *WSConn) Close(status websocket.StatusCode, reason string) error {
	return c.conn.Close(status, reason)
}
