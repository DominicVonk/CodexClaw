package codexsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var tomlBareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func serializeConfigOverrides(config ConfigObject) ([]string, error) {
	if config == nil {
		return nil, nil
	}
	overrides := make([]string, 0, len(config))
	if err := flattenConfigOverrides(config, "", &overrides); err != nil {
		return nil, err
	}
	return overrides, nil
}

func flattenConfigOverrides(value any, prefix string, out *[]string) error {
	object, ok := asObject(value)
	if !ok {
		if prefix == "" {
			return errors.New("codex config overrides must be a plain object")
		}
		rendered, err := toTomlValue(value, prefix)
		if err != nil {
			return err
		}
		*out = append(*out, prefix+"="+rendered)
		return nil
	}
	keys := sortedKeys(object)
	if prefix != "" && len(keys) == 0 {
		*out = append(*out, prefix+"={}")
		return nil
	}
	for _, key := range keys {
		if key == "" {
			return errors.New("codex config override keys must be non-empty strings")
		}
		child := object[key]
		if isNil(child) {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if _, ok := asObject(child); ok {
			if err := flattenConfigOverrides(child, path, out); err != nil {
				return err
			}
			continue
		}
		rendered, err := toTomlValue(child, path)
		if err != nil {
			return err
		}
		*out = append(*out, path+"="+rendered)
	}
	return nil
}

func toTomlValue(value any, path string) (string, error) {
	if value == nil {
		return "", fmt.Errorf("codex config override at %s cannot be null", path)
	}
	switch typed := value.(type) {
	case string:
		return jsonString(typed), nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	case int:
		return strconv.Itoa(typed), nil
	case int8, int16, int32, int64:
		return fmt.Sprintf("%d", reflect.ValueOf(value).Int()), nil
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", reflect.ValueOf(value).Uint()), nil
	case float32:
		return finiteFloat(float64(typed), path)
	case float64:
		return finiteFloat(typed, path)
	case []ConfigValue:
		return tomlArray(typed, path)
	case []any:
		return tomlArray(typed, path)
	case []string:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return tomlArray(items, path)
	case []bool:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return tomlArray(items, path)
	}
	if object, ok := asObject(value); ok {
		keys := sortedKeys(object)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			if key == "" {
				return "", errors.New("codex config override keys must be non-empty strings")
			}
			child := object[key]
			if isNil(child) {
				continue
			}
			rendered, err := toTomlValue(child, path+"."+key)
			if err != nil {
				return "", err
			}
			parts = append(parts, formatTomlKey(key)+" = "+rendered)
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	}
	return "", fmt.Errorf("unsupported codex config override value at %s: %T", path, value)
}

func finiteFloat(value float64, path string) (string, error) {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return "", fmt.Errorf("codex config override at %s must be a finite number", path)
	}
	return strconv.FormatFloat(value, 'f', -1, 64), nil
}

func tomlArray[T any](items []T, path string) (string, error) {
	rendered := make([]string, 0, len(items))
	for index, item := range items {
		value, err := toTomlValue(any(item), fmt.Sprintf("%s[%d]", path, index))
		if err != nil {
			return "", err
		}
		rendered = append(rendered, value)
	}
	return "[" + strings.Join(rendered, ", ") + "]", nil
}

func formatTomlKey(key string) string {
	if tomlBareKey.MatchString(key) {
		return key
	}
	return jsonString(key)
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func asObject(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case ConfigObject:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out, true
	case map[string]any:
		return typed, true
	case map[string]ConfigValue:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	typed := reflect.ValueOf(value)
	switch typed.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return typed.IsNil()
	default:
		return false
	}
}
