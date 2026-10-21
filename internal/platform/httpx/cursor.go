package httpx

import (
	"encoding/base64"
	"encoding/json"
)

// EncodeCursor turns a keyset position into an opaque URL-safe token.
func EncodeCursor(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a token from EncodeCursor; an empty token leaves dst untouched
// and reports false.
func DecodeCursor(s string, dst any) (bool, error) {
	if s == "" {
		return false, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, dst) != nil {
		return false, BadRequest("invalid_cursor", "cursor is invalid")
	}
	return true, nil
}
