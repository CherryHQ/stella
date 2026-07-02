package domaintools

import (
	"encoding/json"
	"fmt"
)

func mustDomainToolInputSchema(data string) map[string]any {
	var schema map[string]any
	if err := json.Unmarshal([]byte(data), &schema); err != nil {
		panic("invalid generated domain tool schema: " + err.Error())
	}
	return schema
}

func decodeDomainToolInput(args map[string]any, dst any, required []string) error {
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
