package automation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/timeline"
)

var fieldPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z0-9_]+){0,7}$`)
var placeholder = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)

func fmtCount(count int) string { return fmt.Sprint(count) }
func object(raw []byte) bool {
	var value map[string]any
	return json.Unmarshal(raw, &value) == nil && value != nil
}
func validCondition(condition Condition) bool {
	return fieldPattern.MatchString(condition.Field) && slices.Contains([]string{"eq", "ne", "contains", "gt", "gte", "lt", "lte"}, condition.Op)
}
func valueAt(fields map[string]any, path string) (any, bool) {
	var current any = fields
	for _, part := range strings.Split(path, ".") {
		values, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = values[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
func fieldsFor(entry timeline.Entry) map[string]any {
	raw, _ := json.Marshal(entry)
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&fields)
	return fields
}
func matches(rule Rule, entry timeline.Entry) bool {
	if entry.Origin == "automation" || entry.Source == "automation" {
		return false
	}
	if rule.Trigger == "timeline" {
		if len(rule.Sources) > 0 && !slices.Contains(rule.Sources, entry.Source) {
			return false
		}
		if len(rule.Kinds) > 0 && !slices.Contains(rule.Kinds, entry.Kind) {
			return false
		}
		if len(rule.Levels) > 0 && !slices.Contains(rule.Levels, entry.Level) {
			return false
		}
	}
	fields := fieldsFor(entry)
	for _, condition := range rule.Conditions {
		value, ok := valueAt(fields, condition.Field)
		if !ok {
			return false
		}
		equal := reflect.DeepEqual(value, condition.Value)
		if left, right := numeric(value), numeric(condition.Value); left != nil && right != nil {
			equal = left.Cmp(right) == 0
		}
		switch condition.Op {
		case "eq":
			if !equal {
				return false
			}
		case "ne":
			if equal {
				return false
			}
		case "contains":
			left, lok := value.(string)
			right, rok := condition.Value.(string)
			if !lok || !rok || !strings.Contains(left, right) {
				return false
			}
		default:
			left, right := numeric(value), numeric(condition.Value)
			if left == nil || right == nil {
				return false
			}
			cmp := left.Cmp(right)
			if condition.Op == "gt" && cmp <= 0 || condition.Op == "gte" && cmp < 0 || condition.Op == "lt" && cmp >= 0 || condition.Op == "lte" && cmp > 0 {
				return false
			}
		}
	}
	return true
}
func numeric(value any) *big.Rat {
	var text string
	switch number := value.(type) {
	case json.Number:
		text = number.String()
	case float64:
		text = fmt.Sprint(number)
	case int:
		text = fmt.Sprint(number)
	case string:
		if len(number) > 100 {
			return nil
		}
		text = number
	default:
		return nil
	}
	if len(text) > 100 {
		return nil
	}
	if position := strings.IndexAny(text, "eE"); position >= 0 {
		exponent, err := strconv.Atoi(text[position+1:])
		if err != nil || exponent > 100 || exponent < -100 {
			return nil
		}
	}
	number, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil
	}
	return number
}
func renderArgs(raw string, entry timeline.Entry) (json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	fields := fieldsFor(entry)
	var walk func(any, int) (any, error)
	walk = func(value any, depth int) (any, error) {
		if depth > 32 {
			return nil, action.Invalid("参数嵌套超过 32 层")
		}
		switch item := value.(type) {
		case map[string]any:
			for key, child := range item {
				next, err := walk(child, depth+1)
				if err != nil {
					return nil, err
				}
				item[key] = next
			}
			return item, nil
		case []any:
			for index, child := range item {
				next, err := walk(child, depth+1)
				if err != nil {
					return nil, err
				}
				item[index] = next
			}
			return item, nil
		case string:
			locations := placeholder.FindAllStringSubmatchIndex(item, -1)
			if len(locations) == 1 && locations[0][0] == 0 && locations[0][1] == len(item) {
				value, ok := valueAt(fields, item[locations[0][2]:locations[0][3]])
				if !ok {
					return nil, action.Invalid("模板引用了缺失字段")
				}
				return value, nil
			}
			failed := false
			rendered := placeholder.ReplaceAllStringFunc(item, func(token string) string {
				parts := placeholder.FindStringSubmatch(token)
				value, ok := valueAt(fields, parts[1])
				if !ok {
					failed = true
					return ""
				}
				switch typed := value.(type) {
				case string:
					return typed
				case nil:
					return ""
				default:
					raw, _ := json.Marshal(value)
					return string(raw)
				}
			})
			if failed {
				return nil, action.Invalid("模板引用了缺失字段")
			}
			if strings.Contains(placeholder.ReplaceAllString(item, ""), "{{") {
				return nil, action.Invalid("模板只支持 {{字段}}")
			}
			return rendered, nil
		default:
			return value, nil
		}
	}
	result, err := walk(root, 0)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if len(encoded) > 64<<10 {
		return nil, action.Invalid("展开后的参数超过 64 KB")
	}
	return encoded, err
}
