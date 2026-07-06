package tools

import (
	"encoding/json"
	"fmt"
)

func MustInputSchema(data string) map[string]any {
	var schema map[string]any
	if err := json.Unmarshal([]byte(data), &schema); err != nil {
		panic("invalid generated tool schema: " + err.Error())
	}
	return schema
}

func DecodeInput(args map[string]any, dst any, required []string) error {
	for _, name := range required {
		if _, ok := args[name]; !ok {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func TruncateText(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	return s[:max], true
}
