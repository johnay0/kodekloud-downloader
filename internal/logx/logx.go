// Package logx is a compact, human-friendly slog handler.
package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

type Handler struct {
	w     io.Writer
	level slog.Leveler
	attrs []slog.Attr
	mu    *sync.Mutex
}

func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(&Handler{w: w, level: level, mu: &sync.Mutex{}})
}

func (h *Handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %-5s %s", r.Time.Format("15:04:05"), r.Level.String(), r.Message)
	var multiline []string
	add := func(a slog.Attr) bool {
		v := a.Value.String()
		if strings.Contains(v, "\n") {
			multiline = append(multiline, fmt.Sprintf("  %s:\n    %s", a.Key, strings.ReplaceAll(v, "\n", "\n    ")))
		} else if v != "" {
			if strings.ContainsAny(v, " \t") {
				v = fmt.Sprintf("%q", v)
			}
			fmt.Fprintf(&b, " %s=%s", a.Key, v)
		}
		return true
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(add)
	b.WriteString("\n")
	for _, m := range multiline {
		b.WriteString(m + "\n")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr{}, h.attrs...), as...)
	return &c
}

func (h *Handler) WithGroup(string) slog.Handler { return h }
