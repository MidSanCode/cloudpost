package state

import (
	"encoding/json"
	"time"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func jsonMarshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
func currentTime() int64 { return time.Now().Unix() }
