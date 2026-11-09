// Package replay 提供测试用的回放连接：按顺序交付预置报文并记录写出的请求。
// 不进入生产热路径。
package replay

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"sync"
)

// Source 是内存中的连接，实现 core.Source。
type Source struct {
	frames chan []byte
	done   chan struct{}
	once   sync.Once

	mu     sync.Mutex
	writes [][]byte
	fail   error
}

// New 创建回放连接，frames 会依次被 Read 交付；之后 Read 阻塞直到 Push 或 Close。
func New(frames ...[]byte) *Source {
	s := &Source{frames: make(chan []byte, 1<<16), done: make(chan struct{})}
	for _, f := range frames {
		s.frames <- f
	}
	return s
}

// Push 追加一帧。
func (s *Source) Push(frame []byte) { s.frames <- frame }

// PushString 追加一帧文本。
func (s *Source) PushString(frame string) { s.frames <- []byte(frame) }

// Read 实现 core.Source。
func (s *Source) Read(ctx context.Context) ([]byte, error) {
	select {
	case f := <-s.frames:
		return f, nil
	case <-s.done:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Write 实现 core.Source，记录写出的帧。
func (s *Source) Write(ctx context.Context, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.writes = append(s.writes, append([]byte(nil), data...))
	return nil
}

// Close 实现 core.Source。
func (s *Source) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// Disconnect 模拟断线：之后 Read 返回 EOF。
func (s *Source) Disconnect() { s.Close() }

// FailWrites 让之后的 Write 返回 err。
func (s *Source) FailWrites(err error) {
	s.mu.Lock()
	s.fail = err
	s.mu.Unlock()
}

// Writes 返回已写出的帧副本。
func (s *Source) Writes() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.writes))
	copy(out, s.writes)
	return out
}

// WritesWithAid 返回 aid 等于给定值的已写帧（解码为 map）。
func (s *Source) WritesWithAid(aid string) []map[string]any {
	var out []map[string]any
	for _, w := range s.Writes() {
		var m map[string]any
		if json.Unmarshal(w, &m) == nil && m["aid"] == aid {
			out = append(out, m)
		}
	}
	return out
}

// LoadSniffer 读取 sniffer 抓包（jsonl），返回 kind=="receive" 的原始报文。
func LoadSniffer(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<28)
	for sc.Scan() {
		var rec struct {
			Kind string `json:"kind"`
			Raw  string `json:"raw"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil, err
		}
		if rec.Kind == "receive" && rec.Raw != "" {
			out = append(out, []byte(rec.Raw))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("replay: no received frames")
	}
	return out, nil
}
