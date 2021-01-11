package server

import (
	"compress/flate"
	"compress/gzip"
	"io"
)

// gzipDecompress implements Decompressor for decompressing gzip content.
func gzipDecompress(r io.Reader) (io.Reader, error) {
	gzipReader, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("problem creating a new gzip reader from io.Reader: %w", err)
	}

	pipeOut, pipeIn := io.Pipe()
	go func() {
		_, err := io.Copy(pipeIn, gzipReader)
		if err != nil {
			pipeIn.CloseWithError(err)
			gzipReader.Close()
			return
		}
		if err := gzipReader.Close(); err != nil {
			pipeIn.CloseWithError(err)
			return
		}
		pipeIn.Close()
	}()
	return pipeOut, nil
}

// defalteDecompress implements Decompressor for decompressing deflate content.
func defalteDecompress(r io.Reader) io.Reader {
	flateReader := flate.NewReader(r)

	pipeOut, pipeIn := io.Pipe()
	go func() {
		_, err := io.Copy(pipeIn, flateReader)
		if err != nil {
			pipeIn.CloseWithError(err)
			flateReader.Close()
			return
		}
		if err := flateReader.Close(); err != nil {
			pipeIn.CloseWithError(err)
			return
		}
		pipeIn.Close()
	}()
	return pipeOut
}
