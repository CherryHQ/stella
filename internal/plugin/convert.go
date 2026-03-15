package plugin

import (
	"encoding/json"
	"fmt"

	"github.com/fastschema/qjs"
)

// goToJSValue converts a Go value to a QJS value using JSON round-trip.
// This is reliable for all serializable types including maps, slices, and structs.
func goToJSValue(ctx *qjs.Context, v any) (*qjs.Value, error) {
	if v == nil {
		return ctx.NewNull(), nil
	}

	// Fast path for primitive types.
	switch val := v.(type) {
	case string:
		return ctx.NewString(val), nil
	case bool:
		return ctx.NewBool(val), nil
	case int:
		return ctx.NewInt32(int32(val)), nil
	case int32:
		return ctx.NewInt32(val), nil
	case int64:
		return ctx.NewFloat64(float64(val)), nil
	case float64:
		return ctx.NewFloat64(val), nil
	}

	// Complex types: JSON round-trip.
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal %T: %w", v, err)
	}
	result, err := ctx.Eval("__conv.js", qjs.Code(fmt.Sprintf("JSON.parse(%q)", string(data))))
	if err != nil {
		return nil, fmt.Errorf("parse value in JS: %w", err)
	}
	return result, nil
}

// jsValueToJSON calls JSON.stringify on a QJS value and returns the JSON string.
func jsValueToJSON(ctx *qjs.Context, val *qjs.Value) (string, error) {
	ctx.Global().SetPropertyStr("__tmp_val", val.Clone())
	result, err := ctx.Eval("__stringify.js", qjs.Code("JSON.stringify(__tmp_val)"))
	if err != nil {
		return "", fmt.Errorf("JSON.stringify: %w", err)
	}
	defer result.Free()
	return result.String(), nil
}

// jsValueToGoMap converts a QJS object value to a Go map via JSON round-trip.
func jsValueToGoMap(ctx *qjs.Context, val *qjs.Value) (map[string]any, error) {
	jsonStr, err := jsValueToJSON(ctx, val)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return m, nil
}
