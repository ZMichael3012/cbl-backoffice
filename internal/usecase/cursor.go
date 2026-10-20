package usecase

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
)

// keysetCursor is the wire form of a page position: the (time, uuid) key bound
// to the filter fingerprint the page was opened with (T-BO3).
type keysetCursor struct {
	// V is the cursor format version.
	V int `json:"v"`
	// T is the key time, RFC 3339 in UTC.
	T string `json:"t"`
	// I is the key identifier.
	I string `json:"i"`
	// F is the filter fingerprint the cursor is bound to.
	F string `json:"f"`
}

// encodeKeysetCursor builds an opaque cursor; a plain-struct marshal failure is
// a programming defect and yields an empty cursor.
func encodeKeysetCursor(version int, fingerprint string, at time.Time, id string) string {
	payload, err := json.Marshal(keysetCursor{
		V: version,
		T: at.UTC().Format(time.RFC3339Nano),
		I: id,
		F: fingerprint,
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

// decodeKeysetCursor parses and validates an opaque cursor; an empty cursor is
// the first page. Every malformed, oversized or foreign-filter cursor is an
// invalid_argument error.
func decodeKeysetCursor(token, fingerprint string, version, maxLen int) (*time.Time, string, error) {
	if token == "" {
		return nil, "", nil
	}
	if len(token) > maxLen {
		return nil, "", invalid("cursor is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, "", invalid("cursor is invalid")
	}
	var parsed keysetCursor
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, "", invalid("cursor is invalid")
	}
	// A cursor is a single JSON value: trailing bytes are rejected.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, "", invalid("cursor is invalid")
	}
	if parsed.V != version || parsed.F != fingerprint {
		return nil, "", invalid("cursor is invalid")
	}
	id, err := uuid.Parse(parsed.I)
	if err != nil {
		return nil, "", invalid("cursor is invalid")
	}
	at, err := time.Parse(time.RFC3339Nano, parsed.T)
	if err != nil {
		return nil, "", invalid("cursor is invalid")
	}
	utc := at.UTC()
	return &utc, id.String(), nil
}
