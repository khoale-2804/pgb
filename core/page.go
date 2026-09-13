package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Opaque keyset-pagination cursors: a value list serialized as compact JSON
// and wrapped in unpadded standard base64. Tokens are opaque to clients —
// treat them as strings and pass them back.

// EncodeCursor serializes vals as JSON and wraps it in unpadded standard
// base64, producing the cursor token handed to clients.
func EncodeCursor(vals []any) (string, error) {
	b, err := json.Marshal(vals)
	if err != nil {
		return "", fmt.Errorf("pgb: encode cursor: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(b), nil
}

// DecodeCursor reverses EncodeCursor into the raw value list. Cursor
// validation — column, direction, table, and value-count checks that raise
// ErrCursorMismatch — is the generated caller's job; this only decodes.
func DecodeCursor(s string) ([]any, error) {
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("pgb: decode cursor: %w", err)
	}
	var vals []any
	if err := json.Unmarshal(b, &vals); err != nil {
		return nil, fmt.Errorf("pgb: decode cursor: %w", err)
	}
	return vals, nil
}

// CursorMismatch builds the error for a cursor that decoded to a different
// number of values than the sort key expects (numWant). errors.Is(err,
// ErrCursorMismatch) holds so callers can restart from the first page.
func CursorMismatch(numWant, numGot int) error {
	return fmt.Errorf("pgb: cursor mismatch: got %d values, want %d: %w", numGot, numWant, ErrCursorMismatch)
}
