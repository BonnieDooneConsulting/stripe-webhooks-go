package webhook

import (
	"bytes"
	"io"
)

func httpBody(b []byte) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(b))
}
