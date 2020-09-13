package server

import (
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
)

// responseWriter implements an http.ResponseWriter that on Write() calls will compress
// the input.
type responseWriter struct {
	io.WriteCloser
	http.ResponseWriter
}

// Write implements io.Write().
func (w responseWriter) Write(b []byte) (int, error) {
	return w.WriteCloser.Write(b)
}

// Close implements io.Close(). This MUST be called after writing completes.
func (w responseWriter) Close() error {
	return w.WriteCloser.Close()
}

// newResponseWriter creates a new responseWriter from a compressor and ResponseWriter. The
// compressor needs to be already writing to rw.Writer (usually done with the compressor's constructor).
func newResponseWriter(compressor io.WriteCloser, rw http.ResponseWriter) responseWriter {
	return responseWriter{compressor, rw}
}

// newGzipResponseWriter returns an http.ResponseWriter that does gzip compression.
func newGzipResponseWriter(w http.ResponseWriter) ResponseWriter {
	gz := gzip.NewWriter(w)
	return newResponseWriter(gz, w)
}

// newDeflateResponseWriter returns an http.ResponseWriter that does deflate compression.
func newDeflateResponseWriter(w http.ResponseWriter) ResponseWriter {
	d, err := flate.NewWriter(w, 3)
	if err != nil {
		panic(err)
	}
	return newResponseWriter(d, w)
}
