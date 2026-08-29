package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
)

func MarshalResult(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func DecodeMap(src map[string]any, dst any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func ParsePage(pageSize int, pageToken string, defaultSize, maxSize int) (int, int, error) {
	limit := defaultSize
	if pageSize != 0 {
		if pageSize < 1 || pageSize > maxSize {
			return 0, 0, fmt.Errorf("invalid page_size")
		}
		limit = pageSize
	}
	if pageToken == "" {
		return limit, 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(pageToken)
	if err != nil {
		return 0, 0, err
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, 0, fmt.Errorf("invalid page_token")
	}
	return limit, offset, nil
}

func PageRows[T any](rows []T, limit, offset int) ([]T, string) {
	if len(rows) > limit {
		return rows[:limit], OffsetToken(offset + limit)
	}
	return rows, ""
}

// OffsetToken encodes a resume offset into the same opaque page token PageRows
// emits, so a caller that owns its own paging (e.g. a PEP that scans past denied
// rows) can hand back a token clients decode identically.
func OffsetToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}
