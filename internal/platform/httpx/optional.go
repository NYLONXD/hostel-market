package httpx

import (
	"bytes"
	"encoding/json"
)

// Optional distinguishes the three states a PATCH field can be in:
// absent (Set=false), explicitly null (Set=true, Null=true) and a value.
type Optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// Ptr returns nil for null, else a pointer to the value. Only meaningful when Set.
func (o Optional[T]) Ptr() *T {
	if o.Null {
		return nil
	}
	v := o.Value
	return &v
}
