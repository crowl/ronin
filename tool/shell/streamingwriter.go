package shell

import "github.com/crowl/ronin/tool"

type streamingWriter struct {
	stream tool.ShellStream
	buffer *limitedBuffer
	emit   func(tool.Artifact) error
}

func newStreamingWriter(stream tool.ShellStream, buffer *limitedBuffer, emit func(tool.Artifact) error) *streamingWriter {
	return &streamingWriter{stream: stream, buffer: buffer, emit: emit}
}

func (w *streamingWriter) Write(p []byte) (int, error) {
	if w.buffer != nil {
		if _, err := w.buffer.Write(p); err != nil {
			return 0, err
		}
	}
	if w.emit != nil && len(p) > 0 {
		artifact := tool.ShellStreamArtifact{Stream: w.stream, Content: string(p)}
		if err := w.emit(artifact); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
