package core

import (
	"context"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
)

// DefaultMaxMessageBytes 是单条解压后消息的默认上限；已知合法初始化包超过 1 MiB。
const DefaultMaxMessageBytes = 64 << 20

// wsSource 是基于 coder/websocket 的生产连接。帧重组与 permessage-deflate 由库处理，
// Read 交付完整、已解压的应用消息。
type wsSource struct {
	conn *websocket.Conn
}

// DialWebsocket 建立一条 WebSocket 连接，显式协商 context takeover 压缩。
func DialWebsocket(ctx context.Context, url string, header http.Header, maxMessageBytes int64) (Source, error) {
	if maxMessageBytes <= 0 {
		maxMessageBytes = DefaultMaxMessageBytes
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", url, err)
	}
	conn.SetReadLimit(maxMessageBytes)
	return &wsSource{conn: conn}, nil
}

// Read 不把 ctx 交给库：coder/websocket 在 Read 的 ctx 取消时直接关闭 TCP、不发关闭帧，
// 会让随后的 Close 无法完成正常关闭握手。路由停止时总会调用 Close，读循环随之结束。
func (s *wsSource) Read(context.Context) ([]byte, error) {
	_, data, err := s.conn.Read(context.Background())
	return data, err
}

func (s *wsSource) Write(ctx context.Context, data []byte) error {
	return s.conn.Write(ctx, websocket.MessageText, data)
}

func (s *wsSource) Close() error {
	return s.conn.Close(websocket.StatusNormalClosure, "")
}
