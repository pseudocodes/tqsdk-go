package webadapter

import (
	"context"
	"time"
)

type Config struct {
	Mode string

	Symbols         []string
	KlineDataLength int
	KlineDurations  []int

	PeekCompatible bool
	MergeWindow    time.Duration
	OutBuffer      int
}

func (c Config) normalize() Config {
	out := c
	if out.Mode == "" {
		out.Mode = "run"
	}
	if out.KlineDataLength <= 0 {
		out.KlineDataLength = 200
	}
	if len(out.KlineDurations) == 0 {
		out.KlineDurations = []int{60}
	}
	if out.MergeWindow <= 0 {
		out.MergeWindow = 30 * time.Millisecond
	}
	if out.OutBuffer <= 0 {
		out.OutBuffer = 256
	}
	return out
}

type Message struct {
	Aid  string           `json:"aid"`
	Data []map[string]any `json:"data"`
	Mode string           `json:"mode,omitempty"`
	TS   int64            `json:"ts,omitempty"`
}

type RuntimeStatusProvider interface {
	SubscribeRuntimeStatus(ctx context.Context) (<-chan map[string]any, error)
	RuntimeStatusSnapshot() map[string]any
}
