package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// syslog priorities, as sd-daemon(3) defines them.
const (
	priErr     = 3
	priWarning = 4
	priInfo    = 6
	priDebug   = 7
)

// journalHandler writes records for systemd-journald. Each line is prefixed
// with "<N>", the syslog priority that journald strips off and records as the
// entry's own priority, so `journalctl -p warning` works on our output. The
// timestamp is left out because the journal keeps one already.
type journalHandler struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
	h   slog.Handler
	w   io.Writer
}

func newJournalHandler(w io.Writer, opts *slog.HandlerOptions) *journalHandler {
	buf := &bytes.Buffer{}
	inner := *opts
	inner.ReplaceAttr = dropTime
	return &journalHandler{
		mu:  &sync.Mutex{},
		buf: buf,
		h:   slog.NewTextHandler(buf, &inner),
		w:   w,
	}
}

func dropTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

func (h *journalHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.h.Enabled(ctx, level)
}

// Handle formats through the inner handler into a shared buffer so that the
// prefix and the line it belongs to reach the writer as one write.
func (h *journalHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.buf.Reset()
	if err := h.h.Handle(ctx, r); err != nil {
		return err
	}
	_, err := fmt.Fprintf(h.w, "<%d>%s", priority(r.Level), h.buf.String())
	return err
}

// WithAttrs and WithGroup keep the lock and buffer shared: every derived
// handler formats into the same buffer.
func (h *journalHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &journalHandler{mu: h.mu, buf: h.buf, h: h.h.WithAttrs(attrs), w: h.w}
}

func (h *journalHandler) WithGroup(name string) slog.Handler {
	return &journalHandler{mu: h.mu, buf: h.buf, h: h.h.WithGroup(name), w: h.w}
}

func priority(l slog.Level) int {
	switch {
	case l >= slog.LevelError:
		return priErr
	case l >= slog.LevelWarn:
		return priWarning
	case l >= slog.LevelInfo:
		return priInfo
	default:
		return priDebug
	}
}
