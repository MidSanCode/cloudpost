package db

import "encoding/json"

// JSONMarshal marshals v (helper shared by packages avoiding import cycles).
func JSONMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// JSONUnmarshal unmarshals b into v.
func JSONUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
