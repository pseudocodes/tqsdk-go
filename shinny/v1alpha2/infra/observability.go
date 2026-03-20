package infra

import "log/slog"

type Observability struct {
	Logger *slog.Logger
}

func NewObservability(logger *slog.Logger) *Observability {
	if logger == nil {
		logger = slog.Default()
	}
	return &Observability{Logger: logger}
}
