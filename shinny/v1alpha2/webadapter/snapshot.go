package webadapter

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

func BuildInitialSnapshot(client api.Client, cfg Config, subscribed []map[string]any) Message {
	data := BuildTqWebInitialData(client, cfg, subscribed)
	return Message{
		Aid:  "rtn_data",
		Data: []map[string]any{data},
		Mode: cfg.Mode,
		TS:   time.Now().UnixNano(),
	}
}

func BuildTqWebInitialData(client api.Client, cfg Config, subscribed []map[string]any) map[string]any {
	cfg = cfg.normalize()
	filePath := ""
	if len(os.Args) > 0 {
		if abs, err := filepath.Abs(os.Args[0]); err == nil {
			filePath = abs
		}
	}
	fileName := filepath.Base(filePath)

	action := map[string]any{
		"mode":          cfg.Mode,
		"md_url_status": false,
		"user_name":     "",
		"file_path":     upperDriverPrefix(filePath),
		"file_name":     fileName,
		"accounts":      map[string]any{},
	}

	if client != nil && client.Auth() != nil {
		if s, ok := client.Auth().Session(); ok {
			action["user_name"] = s.UserName
		}
	}

	data := map[string]any{
		"action":           action,
		"trade":            map[string]any{},
		"subscribed":       subscribed,
		"draw_chart_datas": map[string]any{},
		"snapshots":        map[string]any{},
	}

	if client != nil && client.Market() != nil {
		if p, ok := client.Market().(RuntimeStatusProvider); ok {
			rt := p.RuntimeStatusSnapshot()
			mergeDiff(data, rt)
		}
	}
	return data
}

func upperDriverPrefix(path string) string {
	if len(path) < 2 {
		return path
	}
	if path[1] != ':' {
		return path
	}
	return strings.ToUpper(path[:1]) + path[1:]
}
