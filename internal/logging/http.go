package logging

import (
	"net/http"
	"time"
)

// RequestMiddleware wraps next so every served request emits one line on the
// http module logger: method, path, status, response size, and duration.
// Request and response bodies are never logged, and the query string carries
// peer task text, which is not logged either.
//
// The route label is the request path template the caller supplies, so metrics
// and logs collapse per route rather than per URL.
func RequestMiddleware(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		log := HTTP()
		var ev = log.Info()
		switch {
		case sw.status >= http.StatusInternalServerError:
			ev = log.Error()
		case sw.status >= http.StatusBadRequest:
			ev = log.Warn()
		}
		ev.Str("route", route).
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", sw.status).
			Int("bytes", sw.bytes).
			Dur("duration", time.Since(start)).
			Msg("request")
	})
}

// statusWriter records the status code and body size a handler wrote while
// passing writes through unchanged.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

// Unwrap exposes the wrapped writer to http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
