package server

import (
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
)

var doNotCompress = map[string]bool{
	// Compression Only
	// https://en.wikipedia.org/wiki/List_of_archive_formats#Compression%20only
	".bz2":   true,
	".gz":    true,
	".lz4":   true,
	".lzma":  true,
	".lzo":   true,
	".rz":    true,
	".sfark": true,
	".sz":    true,
	".?Q?":   true,
	"?Z?":    true,
	".xz":    true,
	".z":     true,
	".Z":     true,
	".zst":   true,
	".??_":   true,
	// Archiving and compression
	// https://en.wikipedia.org/wiki/List_of_archive_formats#Archiving_and_compression
	".7z":      true,
	".s7z":     true,
	".ace":     true,
	".afa":     true,
	".alz":     true,
	".apk":     true,
	".arc":     true,
	".ark":     true,
	".cdx":     true,
	".arj":     true,
	".b1":      true,
	".b6z":     true,
	".ba":      true,
	".bh":      true,
	".cab":     true,
	".car":     true,
	".cfs":     true,
	".cpt":     true,
	".dar":     true,
	".dd":      true,
	".dgc":     true,
	".ear":     true,
	".gca":     true,
	".ha":      true,
	".hki":     true,
	".ice":     true,
	".jar":     true,
	".kgb":     true,
	".lzh":     true,
	".lha":     true,
	".lzx":     true,
	".pak":     true,
	".partimg": true,
	".paq6":    true,
	".paq7":    true,
	".paq8":    true,
	".pea":     true,
	".pim":     true,
	".pit":     true,
	".qda":     true,
	".rar":     true,
	".rk":      true,
	".sda":     true,
	".sea":     true,
	".sen":     true,
	".sfx":     true,
	".shk":     true,
	".sit":     true,
	".sitx":    true,
	".sqx":     true,
	".tar.gz":  true,
	".tgz":     true,
	".tar.Z":   true,
	".tar.bz2": true,
	".tbz2":    true,
	".tar.lz":  true,
	".tlz ":    true,
	".tar.xz":  true,
	".txz":     true,
	".uc ":     true,
	".uc0 ":    true,
	".uc2 ":    true,
	".ucn ":    true,
	".ur2":     true,
	".ue2":     true,
	".uca":     true,
	".uha":     true,
	".war":     true,
	".wim":     true,
	".xar":     true,
	".xp3":     true,
	".yz1":     true,
	".zip":     true,
	".zipx":    true,
	".zoo":     true,
	".zpaq":    true,
	".zz":      true,
	// Compressed image foramts
	".gif":  true,
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".jp2":  true,
	".webp": true,
	// Compreseed audio formats
	".aac":  true,
	".mp3":  true,
	".wma":  true,
	".flac": true,
	".m4a":  true,
	".caf":  true,
	// Compressed video formats
	".mp4":  true,
	".m4p":  true,
	".m4v":  true,
	".svi":  true,
	".3gp":  true,
	".3g2":  true,
	".webm": true,
	".flv":  true,
	".f4v":  true,
	".f4p":  true,
	".f4a":  true,
	".f4b":  true,
	".vob":  true,
	".ogv":  true,
	".oog":  true,
	".mov":  true,
	".qt":   true,
	".rmvb": true,
}

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
