package shell

import "bytes"

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int64
	written   int64
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)

	if b.limit <= 0 {
		b.written += int64(n)
		return n, nil
	}

	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		b.written += int64(n)
		return n, nil
	}

	if int64(len(p)) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
	} else {
		b.buf.Write(p)
	}

	b.written += int64(n)
	return n, nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

func outputLimit(requested, fallback int64) int64 {
	limit := fallback
	if requested > 0 {
		limit = requested
	}
	if limit > maxOutputBytes {
		return maxOutputBytes
	}
	return limit
}
