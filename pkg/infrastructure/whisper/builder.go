package whisper

import (
	"errors"
	"strconv"
	"strings"
)

type whisperServerBuilder struct {
	modelPath string
	host      string
	port      int
}

func newWhisperServerBuilder() *whisperServerBuilder {
	return &whisperServerBuilder{
		host: "127.0.0.1",
	}
}

func (b *whisperServerBuilder) withModel(path string) *whisperServerBuilder {
	b.modelPath = strings.TrimSpace(path)
	return b
}

func (b *whisperServerBuilder) withBind(host string, port int) *whisperServerBuilder {
	if clean := strings.TrimSpace(host); clean != "" {
		b.host = clean
	}
	b.port = port
	return b
}

func (b *whisperServerBuilder) buildArgs() ([]string, error) {
	if b.modelPath == "" {
		return nil, errors.New("empty whisper model path")
	}
	if b.port <= 0 || b.port > 65535 {
		return nil, errors.New("invalid local port for whisper-server")
	}
	return []string{
		"--model", b.modelPath,
		"--host", b.host,
		"--port", strconv.Itoa(b.port),
	}, nil
}
