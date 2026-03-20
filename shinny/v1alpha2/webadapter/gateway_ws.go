package webadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type GatewayConfig struct {
	Addr         string
	WSPath       string
	SnapshotPath string

	// WebDir is the path to the static web UI directory (index.html, css/, js/, etc.).
	// When set, the gateway serves "/" → index.html, "/web/" → static assets,
	// and "/url" → JSON with ins_url/md_url/access_token — matching tqwebhelper.
	WebDir string

	// URLResponse is the JSON payload returned by the /url endpoint.
	// Typically contains ins_url, md_url, access_token for the frontend.
	URLResponse map[string]any
}

type Gateway struct {
	adapter *Adapter
	cfg     GatewayConfig

	mu     sync.Mutex
	server *http.Server
}

func NewGateway(adapter *Adapter, cfg GatewayConfig) *Gateway {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:9876"
	}
	if cfg.WSPath == "" {
		cfg.WSPath = "/ws"
	}
	if cfg.SnapshotPath == "" {
		cfg.SnapshotPath = "/snapshot"
	}
	return &Gateway{adapter: adapter, cfg: cfg}
}

func (g *Gateway) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc(g.cfg.WSPath, g.handleWS)
	mux.HandleFunc(g.cfg.SnapshotPath, g.handleSnapshot)

	// Serve static web UI files (tqwebhelper compatible).
	if g.cfg.WebDir != "" {
		webFS := http.FileServer(http.Dir(g.cfg.WebDir))
		mux.Handle("/web/", http.StripPrefix("/web", webFS))
		mux.HandleFunc("/", g.handleIndex)
		mux.HandleFunc("/index.html", g.handleIndex)
	}

	// /url endpoint — returns ins_url, md_url, access_token for the frontend.
	if g.cfg.URLResponse != nil {
		mux.HandleFunc("/url", g.handleURL)
	}

	srv := &http.Server{
		Addr:    g.cfg.Addr,
		Handler: mux,
	}

	g.mu.Lock()
	g.server = srv
	g.mu.Unlock()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	err := srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (g *Gateway) Close(ctx context.Context) error {
	g.mu.Lock()
	srv := g.server
	g.server = nil
	g.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (g *Gateway) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	msg := g.adapter.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msg)
}

func (g *Gateway) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()
	msgCh, err := g.adapter.Subscribe(ctx)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, err.Error())
		return
	}

	if g.adapter.cfg.PeekCompatible {
		g.handleWSPeek(ctx, conn, msgCh)
		return
	}
	g.handleWSPush(ctx, conn, msgCh)
}

func (g *Gateway) handleWSPush(ctx context.Context, conn *websocket.Conn, msgCh <-chan Message) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			if err := wsjson.Write(ctx, conn, msg); err != nil {
				return
			}
		}
	}
}

func (g *Gateway) handleWSPeek(ctx context.Context, conn *websocket.Conn, msgCh <-chan Message) {
	// Send initial snapshot unconditionally.
	if first, ok := <-msgCh; ok {
		_ = wsjson.Write(ctx, conn, first)
	}

	// peekCh signals that the frontend sent a peek_message and is waiting
	// for the next rtn_data.  Buffer of 1 is enough — the frontend won't
	// send a second peek before receiving a reply.
	peekCh := make(chan struct{}, 1)

	// Read loop: forward peek_message signals.
	go func() {
		defer func() { close(peekCh) }()
		for {
			var req map[string]any
			if err := wsjson.Read(ctx, conn, &req); err != nil {
				return
			}
			if aid, _ := req["aid"].(string); aid == "peek_message" {
				select {
				case peekCh <- struct{}{}:
				default: // already signalled
				}
			}
		}
	}()

	pending := map[string]any{}
	peeked := false // true when a peek_message is waiting for reply

	for {
		// If we have a pending peek AND accumulated diffs, reply now.
		if peeked && len(pending) > 0 {
			resp := Message{Aid: "rtn_data", Data: []map[string]any{cloneAnyMap(pending)}, Mode: g.adapter.cfg.Mode, TS: time.Now().UnixNano()}
			pending = map[string]any{}
			peeked = false
			if err := wsjson.Write(ctx, conn, resp); err != nil {
				return
			}
			continue
		}

		// Otherwise block until we get a peek or a diff (or context done).
		select {
		case <-ctx.Done():
			return
		case _, ok := <-peekCh:
			if !ok {
				return
			}
			peeked = true
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			for _, d := range msg.Data {
				mergeDiff(pending, d)
			}
		}
	}
}

func (g *Gateway) URL() string {
	return fmt.Sprintf("ws://%s%s", g.cfg.Addr, g.cfg.WSPath)
}

func (g *Gateway) handleIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, g.cfg.WebDir+"/index.html")
}

func (g *Gateway) handleURL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(g.cfg.URLResponse)
}
