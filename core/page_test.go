package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCursorRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		vals []any
	}{
		{name: "single string", vals: []any{"5027"}},
		{name: "composite pair", vals: []any{float64(7), float64(5027)}},
		{name: "mixed types", vals: []any{"a", 3.5, true, nil, "", -12.25}},
		{name: "empty list", vals: []any{}},
		{name: "nil list", vals: nil},
		{name: "unicode", vals: []any{"héllo — 世界"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok, err := EncodeCursor(tt.vals)
			if err != nil {
				t.Fatalf("EncodeCursor: %v", err)
			}
			// Unpadded standard base64: no '=' padding characters.
			if strings.Contains(tok, "=") {
				t.Errorf("token %q contains padding", tok)
			}
			got, err := DecodeCursor(tok)
			if err != nil {
				t.Fatalf("DecodeCursor: %v", err)
			}
			if len(got) == 0 && len(tt.vals) == 0 {
				return // nil vs empty decode equivalent
			}
			if !reflect.DeepEqual(got, tt.vals) {
				t.Errorf("roundtrip mismatch\n got: %#v\nwant: %#v", got, tt.vals)
			}
		})
	}
}

func TestEncodeCursorErrors(t *testing.T) {
	if _, err := EncodeCursor([]any{make(chan int)}); err == nil {
		t.Error("want error for unserializable value")
	}
}

func TestDecodeCursorErrors(t *testing.T) {
	t.Run("invalid base64", func(t *testing.T) {
		if _, err := DecodeCursor("!!!not-base64!!!"); err == nil {
			t.Error("want error for invalid base64")
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		// base64("}{") — decodes fine, unmarshals badly
		if _, err := DecodeCursor("fXs"); err == nil {
			t.Error("want error for invalid json payload")
		}
	})
	t.Run("valid roundtrip still works", func(t *testing.T) {
		tok, err := EncodeCursor([]any{"x"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeCursor(tok); err != nil {
			t.Fatalf("DecodeCursor: %v", err)
		}
	})
}

func TestCursorMismatch(t *testing.T) {
	err := CursorMismatch(2, 3)
	if err == nil {
		t.Fatal("want non-nil error")
	}
	if !errors.Is(err, ErrCursorMismatch) {
		t.Errorf("errors.Is(err, ErrCursorMismatch) = false; err = %v", err)
	}
	for _, want := range []string{"got 3", "want 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q missing %q", err.Error(), want)
		}
	}
}
