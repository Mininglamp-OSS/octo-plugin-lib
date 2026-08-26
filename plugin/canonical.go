package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	maxCanonicalNumberLength    = 10_240
	maxCanonicalNumberExpansion = 10_240
)

// CanonicalJSON produces UTF-8 JSON with sorted object keys and exact decimal
// number normalization. Unlike float-based encoders it does not lose integer
// precision. Duplicate keys and impractically large numeric exponents fail.
func CanonicalJSON(raw []byte) ([]byte, error) {
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := appendCanonicalDocument(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// ComputePluginHash implements the frozen sha256(manifest_json + plugin_json)
// contract over canonical JSON bytes.
func ComputePluginHash(manifestJSON, pluginJSON []byte) (string, error) {
	manifest, err := CanonicalJSON(manifestJSON)
	if err != nil {
		return "", withPath(err, "manifest_json")
	}
	if isJSONNull(pluginJSON) {
		pluginJSON = []byte("null")
	}
	packageValue, err := canonicalPackageJSON(pluginJSON)
	if err != nil {
		return "", withPath(err, "plugin_json")
	}
	return computePluginHashCanonical(manifest, packageValue), nil
}

func canonicalPackageJSON(raw []byte) ([]byte, error) {
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if ok {
		if attachments, ok := object["attachments"].([]any); ok {
			sort.SliceStable(attachments, func(i, j int) bool {
				leftObject, leftObjectOK := attachments[i].(map[string]any)
				rightObject, rightObjectOK := attachments[j].(map[string]any)
				left, leftOK := leftObject["path"].(string)
				right, rightOK := rightObject["path"].(string)
				leftOK = leftObjectOK && leftOK
				rightOK = rightObjectOK && rightOK
				if leftOK != rightOK {
					return leftOK
				}
				return leftOK && rightOK && left < right
			})
		}
	}
	var output bytes.Buffer
	if err := appendCanonicalDocument(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func computePluginHashCanonical(manifest, packageValue []byte) string {
	hash := sha256.New()
	hash.Write(manifest)
	hash.Write(packageValue)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func appendCanonicalDocument(output *bytes.Buffer, value any) error {
	numberExpansion := 0
	return appendCanonical(output, value, &numberExpansion)
}

func appendCanonical(output *bytes.Buffer, value any, numberExpansion *int) error {
	switch current := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		output.WriteString(strconv.FormatBool(current))
	case string:
		encoded, _ := json.Marshal(current)
		output.Write(encoded)
	case json.Number:
		raw := current.String()
		number, err := canonicalNumber(raw)
		if err != nil {
			return invalid(CodeInvalidJSON, "", err.Error())
		}
		if growth := len(number) - len(raw); growth > 0 {
			if growth > maxCanonicalNumberExpansion-*numberExpansion {
				return invalid(CodeInvalidJSON, "", "canonical JSON number expansion exceeds 10240 characters")
			}
			*numberExpansion += growth
		}
		output.WriteString(number)
	case []any:
		output.WriteByte('[')
		for index, item := range current {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := appendCanonical(output, item, numberExpansion); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			output.Write(encoded)
			output.WriteByte(':')
			if err := appendCanonical(output, current[key], numberExpansion); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

func canonicalNumber(value string) (string, error) {
	if len(value) > maxCanonicalNumberLength {
		return "", fmt.Errorf("JSON number is too long")
	}
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	exponent := 0
	if position := strings.IndexAny(value, "eE"); position >= 0 {
		parsed, err := strconv.Atoi(value[position+1:])
		if err != nil || parsed < -10000 || parsed > 10000 {
			return "", fmt.Errorf("JSON number exponent is outside the supported range")
		}
		exponent = parsed
		value = value[:position]
	}
	fractionDigits := 0
	if position := strings.IndexByte(value, '.'); position >= 0 {
		fractionDigits = len(value) - position - 1
		value = value[:position] + value[position+1:]
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0", nil
	}
	scale := fractionDigits - exponent
	for scale > 0 && strings.HasSuffix(value, "0") {
		value = strings.TrimSuffix(value, "0")
		scale--
	}
	var normalized string
	switch {
	case scale <= 0:
		normalized = value + strings.Repeat("0", -scale)
	case scale >= len(value):
		normalized = "0." + strings.Repeat("0", scale-len(value)) + value
	default:
		normalized = value[:len(value)-scale] + "." + value[len(value)-scale:]
	}
	if negative {
		normalized = "-" + normalized
	}
	if len(normalized) > maxCanonicalNumberLength {
		return "", fmt.Errorf("canonical JSON number is too long")
	}
	return normalized, nil
}
