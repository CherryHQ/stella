package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// DecodeInputStrict decodes tool arguments and rejects any field the target
// struct does not declare. Split tools carry an exact schema
// (additionalProperties:false), so a field the schema forbids must not be
// silently dropped here — a typo like "titel" would otherwise cost the model a
// whole turn to notice. DecodeInput stays lenient for union tools, whose
// hoisted schema deliberately accepts every action's fields.
func DecodeInputStrict(args map[string]any, dst any, required []string) error {
	for _, name := range required {
		if _, ok := args[name]; !ok {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if field, ok := unknownField(err); ok {
			return fmt.Errorf("unknown field %q; this tool accepts: %s", field, strings.Join(acceptedFields(dst), ", "))
		}
		return err
	}
	return nil
}

// unknownFieldRE matches encoding/json's DisallowUnknownFields error, which
// names the offending key but not the struct it belongs to.
var unknownFieldRE = regexp.MustCompile(`unknown field "([^"]*)"`)

func unknownField(err error) (string, bool) {
	m := unknownFieldRE.FindStringSubmatch(err.Error())
	if m == nil {
		return "", false
	}
	return m[1], true
}

// acceptedFields lists every JSON name the target accepts, in schema order:
// top-level names first, then batch item names as "items[].field", so the
// error tells the model where the field it meant actually lives.
func acceptedFields(dst any) []string {
	v := reflect.ValueOf(dst)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	names := collectFieldNames(v.Type(), "", 0)
	sort.Strings(names)
	return names
}

// collectFieldNames walks a decoded input struct. Depth is bounded because tool
// inputs are one or two levels deep by construction (a flat object, or a batch
// wrapper around item objects); anything deeper is a map[string]any and has no
// declared fields to list.
func collectFieldNames(t reflect.Type, prefix string, depth int) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || depth > 2 {
		return nil
	}
	var out []string
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		out = append(out, prefix+name)
		elem := field.Type
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		switch elem.Kind() {
		case reflect.Slice, reflect.Array:
			out = append(out, collectFieldNames(elem.Elem(), prefix+name+"[].", depth+1)...)
		case reflect.Struct:
			out = append(out, collectFieldNames(elem, prefix+name+".", depth+1)...)
		}
	}
	return out
}
