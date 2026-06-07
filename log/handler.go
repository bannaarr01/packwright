package log

import (
	"io"
	"log/slog"
	"strings"
)

// Redact is the package-level redaction hook. It is invoked with each
// serialized log record (the bytes slog hands to the writer) and returns a
// possibly modified copy with secrets removed. The default value is the
// identity function so log.go can be used in tests and as a library without
// pulling in the redactor. MVP-2 PR-06 installs the real implementation in an
// init() in redact.go; the indirection keeps the redactor in its own file
// and avoids a hard dependency from log.go on the pattern set.
var Redact = func(b []byte) []byte { return b }

// redactWriter wraps an io.Writer so every Write is filtered through Redact
// before reaching the underlying writer. Errors from the underlying writer
// propagate as (0, err); successful writes report len(p) so io.Writer's
// contract holds even when the redacted payload differs in length from p.
type redactWriter struct{ w io.Writer }

// Write applies the package-level Redact function to p and forwards the
// result to the underlying writer.
func (rw redactWriter) Write(p []byte) (int, error) {
	if _, err := rw.w.Write(Redact(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// newHandler builds the slog.Handler used by New. It selects either
// slog.NewJSONHandler or slog.NewTextHandler based on format ("json" picks
// JSON; anything else picks text, including the empty string). The writer is
// wrapped in redactWriter so every record passes through the package-level
// Redact hook before it reaches w.
func newHandler(w io.Writer, level slog.Level, format string) slog.Handler {
	w = redactWriter{w: w}
	hopts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(format, "json") {
		return slog.NewJSONHandler(w, hopts)
	}
	return slog.NewTextHandler(w, hopts)
}
