package pluginhost

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/vaayne/anna/internal/pluginapi"
)

func writeEnvelope(w io.Writer, env pluginapi.Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write envelope: %w", err)
	}
	return nil
}

func readEnvelope(r *bufio.Reader) (pluginapi.Envelope, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return pluginapi.Envelope{}, err
	}

	var env pluginapi.Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return pluginapi.Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	return env, nil
}
