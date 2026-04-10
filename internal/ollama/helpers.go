package ollama

import (
	"bytes"
	"encoding/json"
	"io"
)

// jsonBody encodes v as JSON and returns an io.Reader.
func jsonBody(v interface{}) io.Reader {
	data, err := json.Marshal(v)
	if err != nil {
		return bytes.NewReader([]byte("{}"))
	}
	return bytes.NewReader(data)
}
