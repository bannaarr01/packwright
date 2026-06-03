package log

import (
	"io"
	"log/slog"
	"strings"
)

// newHandler builds the slog.Handler used by New. It selects either
// slog.NewJSONHandler or slog.NewTextHandler based on format ("json" picks
// JSON; anything else picks text, including the empty string).
//
// TODO(MVP-2 PR-06): wrap the returned handler in the redactor handler
// specified by ADR-0018. The redactor will sit between this constructor and
// the writer, scrubbing AWS access keys, secret keys, session tokens, JWTs,
// and any attribute whose form-schema type was "secret" from records before
// they are serialized. Until then this is a clean pass-through.
func newHandler(w io.Writer, level slog.Level, format string) slog.Handler {
	hopts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(format, "json") {
		return slog.NewJSONHandler(w, hopts)
	}
	return slog.NewTextHandler(w, hopts)
}
