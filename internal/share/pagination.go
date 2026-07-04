package share

import (
	"encoding/base64"
	"strconv"
)

func pageRows[T any](rows []T, limit, offset int) ([]T, string) {
	if len(rows) <= limit {
		return rows, ""
	}
	return rows[:limit], base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + limit)))
}
