package chat

import (
	"encoding"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"

	"github.com/cloudwego/eino/schema"
)

var (
	errModelOutputEstimateExceeded = errors.New("chat: model output estimate exceeded")
	errUnsafeModelOutputValue      = errors.New("chat: unsupported model output value")
)

// Extra is extension metadata, not the assistant body. Keep its structural
// traversal materially below the byte ceiling so a custom provider cannot burn
// CPU with hundreds of thousands of empty arrays/maps that account for almost
// no semantic output.
const maxModelOutputEstimateNodes = 1 << 16
const maxModelOutputEstimateDepth = 64

// outputSizeEstimator counts the caller-controlled strings and byte payloads
// carried by a model response. It deliberately stops at the active budget so a
// custom model cannot force a second, unbounded allocation merely to measure an
// already-oversized response.
type outputSizeEstimator struct {
	limit  int64
	total  int64
	nodes  int
	depth  int
	active map[outputVisit]struct{}
}

type outputVisit struct {
	kind reflect.Kind
	ptr  uintptr
}

func modelMessageOutputBytes(message *schema.Message, limit int64) (int64, error) {
	if message == nil {
		return 0, nil
	}
	if limit < 0 {
		return 0, errModelOutputEstimateExceeded
	}
	s := &outputSizeEstimator{limit: limit, active: make(map[outputVisit]struct{})}

	if err := s.addStrings(message.Content, message.Name, message.ToolCallID, message.ToolName, message.ReasoningContent); err != nil {
		return 0, err
	}
	for _, part := range message.MultiContent {
		if err := s.addStrings(string(part.Type), part.Text); err != nil {
			return 0, err
		}
		if part.ImageURL != nil {
			if err := s.addStrings(part.ImageURL.URL, part.ImageURL.URI, string(part.ImageURL.Detail), part.ImageURL.MIMEType); err != nil {
				return 0, err
			}
			if err := s.addExtra(part.ImageURL.Extra); err != nil {
				return 0, err
			}
		}
		if part.AudioURL != nil {
			if err := s.addStrings(part.AudioURL.URL, part.AudioURL.URI, part.AudioURL.MIMEType); err != nil {
				return 0, err
			}
			if err := s.addExtra(part.AudioURL.Extra); err != nil {
				return 0, err
			}
		}
		if part.VideoURL != nil {
			if err := s.addStrings(part.VideoURL.URL, part.VideoURL.URI, part.VideoURL.MIMEType); err != nil {
				return 0, err
			}
			if err := s.addExtra(part.VideoURL.Extra); err != nil {
				return 0, err
			}
		}
		if part.FileURL != nil {
			if err := s.addStrings(part.FileURL.URL, part.FileURL.URI, part.FileURL.MIMEType, part.FileURL.Name); err != nil {
				return 0, err
			}
			if err := s.addExtra(part.FileURL.Extra); err != nil {
				return 0, err
			}
		}
	}
	for i := range message.UserInputMultiContent {
		if err := s.addMessageInputPart(&message.UserInputMultiContent[i]); err != nil {
			return 0, err
		}
	}
	for i := range message.AssistantGenMultiContent {
		if err := s.addMessageOutputPart(&message.AssistantGenMultiContent[i]); err != nil {
			return 0, err
		}
	}
	for i := range message.ToolCalls {
		call := &message.ToolCalls[i]
		if err := s.addStrings(call.ID, call.Type, call.Function.Name, call.Function.Arguments); err != nil {
			return 0, err
		}
		if err := s.addExtra(call.Extra); err != nil {
			return 0, err
		}
	}
	if message.ResponseMeta != nil {
		if err := s.addString(message.ResponseMeta.FinishReason); err != nil {
			return 0, err
		}
		if message.ResponseMeta.LogProbs != nil {
			for i := range message.ResponseMeta.LogProbs.Content {
				prob := &message.ResponseMeta.LogProbs.Content[i]
				if err := s.addString(prob.Token); err != nil {
					return 0, err
				}
				if err := s.addInt64Bytes(prob.Bytes); err != nil {
					return 0, err
				}
				for j := range prob.TopLogProbs {
					top := &prob.TopLogProbs[j]
					if err := s.addString(top.Token); err != nil {
						return 0, err
					}
					if err := s.addInt64Bytes(top.Bytes); err != nil {
						return 0, err
					}
				}
			}
		}
	}
	if err := s.addExtra(message.Extra); err != nil {
		return 0, err
	}
	return s.total, nil
}

func (s *outputSizeEstimator) addMessageInputPart(part *schema.MessageInputPart) error {
	if part == nil {
		return nil
	}
	if err := s.addStrings(string(part.Type), part.Text); err != nil {
		return err
	}
	if part.Image != nil {
		if err := s.addMessagePartCommon(&part.Image.MessagePartCommon); err != nil {
			return err
		}
		if err := s.addString(string(part.Image.Detail)); err != nil {
			return err
		}
	}
	if part.Audio != nil {
		if err := s.addMessagePartCommon(&part.Audio.MessagePartCommon); err != nil {
			return err
		}
	}
	if part.Video != nil {
		if err := s.addMessagePartCommon(&part.Video.MessagePartCommon); err != nil {
			return err
		}
	}
	if part.File != nil {
		if err := s.addMessagePartCommon(&part.File.MessagePartCommon); err != nil {
			return err
		}
		if err := s.addString(part.File.Name); err != nil {
			return err
		}
	}
	if part.ToolSearchResult != nil {
		for _, tool := range part.ToolSearchResult.Tools {
			if tool == nil {
				continue
			}
			if err := s.addStrings(tool.Name, tool.Desc); err != nil {
				return err
			}
			if err := s.addExtra(tool.Extra); err != nil {
				return err
			}
			// ParamsOneOf hides its backing graph and can contain recursive JSON
			// schemas. There is no bounded traversal API, so fail closed instead
			// of serializing it into an unbounded temporary buffer.
			if tool.ParamsOneOf != nil {
				return errUnsafeModelOutputValue
			}
		}
	}
	return s.addExtra(part.Extra)
}

func (s *outputSizeEstimator) addMessageOutputPart(part *schema.MessageOutputPart) error {
	if part == nil {
		return nil
	}
	if err := s.addStrings(string(part.Type), part.Text); err != nil {
		return err
	}
	if part.Image != nil {
		if err := s.addMessagePartCommon(&part.Image.MessagePartCommon); err != nil {
			return err
		}
	}
	if part.Audio != nil {
		if err := s.addMessagePartCommon(&part.Audio.MessagePartCommon); err != nil {
			return err
		}
	}
	if part.Video != nil {
		if err := s.addMessagePartCommon(&part.Video.MessagePartCommon); err != nil {
			return err
		}
	}
	if part.Reasoning != nil {
		if err := s.addStrings(part.Reasoning.Text, part.Reasoning.Signature); err != nil {
			return err
		}
	}
	return s.addExtra(part.Extra)
}

func (s *outputSizeEstimator) addMessagePartCommon(part *schema.MessagePartCommon) error {
	if part == nil {
		return nil
	}
	if part.URL != nil {
		if err := s.addString(*part.URL); err != nil {
			return err
		}
	}
	if part.Base64Data != nil {
		if err := s.addString(*part.Base64Data); err != nil {
			return err
		}
	}
	if err := s.addString(part.MIMEType); err != nil {
		return err
	}
	return s.addExtra(part.Extra)
}

func (s *outputSizeEstimator) addInt64Bytes(values []int64) error {
	if len(values) > math.MaxInt64/8 {
		return errModelOutputEstimateExceeded
	}
	return s.add(int64(len(values)) * 8)
}

func (s *outputSizeEstimator) addStrings(values ...string) error {
	for _, value := range values {
		if err := s.addString(value); err != nil {
			return err
		}
	}
	return nil
}

func (s *outputSizeEstimator) addString(value string) error {
	return s.add(int64(len(value)))
}

func (s *outputSizeEstimator) add(amount int64) error {
	if amount < 0 || amount > s.limit-s.total {
		return errModelOutputEstimateExceeded
	}
	s.total += amount
	return nil
}

func (s *outputSizeEstimator) addExtra(extra map[string]any) error {
	if len(extra) == 0 {
		return nil
	}
	return s.addJSONValue(reflect.ValueOf(extra))
}

func (s *outputSizeEstimator) addJSONValue(value reflect.Value) error {
	if !value.IsValid() {
		return s.add(4)
	}
	s.depth++
	if s.depth > maxModelOutputEstimateDepth {
		s.depth--
		return errUnsafeModelOutputValue
	}
	defer func() { s.depth-- }()
	s.nodes++
	if s.nodes > maxModelOutputEstimateNodes {
		return errUnsafeModelOutputValue
	}

	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return s.add(4)
		}
		value = value.Elem()
		if !value.IsValid() {
			return s.add(4)
		}
	}

	if value.Type() == reflect.TypeOf(json.RawMessage{}) {
		if value.IsNil() {
			return s.add(4)
		}
		if err := s.add(int64(value.Len())); err != nil {
			return err
		}
		if !json.Valid(value.Bytes()) {
			return errUnsafeModelOutputValue
		}
		return nil
	}
	if value.Type() == reflect.TypeOf(json.Number("")) {
		number := value.Interface().(json.Number)
		raw := number.String()
		parsed, err := number.Float64()
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || strings.TrimSpace(raw) != raw || !json.Valid([]byte(raw)) {
			return errUnsafeModelOutputValue
		}
		return s.addString(raw)
	}
	if value.CanInterface() {
		if _, ok := value.Interface().(json.Marshaler); ok {
			return errUnsafeModelOutputValue
		}
		if _, ok := value.Interface().(encoding.TextMarshaler); ok {
			return errUnsafeModelOutputValue
		}
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return s.add(4)
		}
		return s.withActive(value, func() error { return s.addJSONValue(value.Elem()) })
	case reflect.String:
		return s.addString(value.String())
	case reflect.Bool:
		if value.Bool() {
			return s.add(4)
		}
		return s.add(5)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return s.add(20)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return s.add(20)
	case reflect.Float32, reflect.Float64:
		if number := value.Float(); math.IsNaN(number) || math.IsInf(number, 0) {
			return errUnsafeModelOutputValue
		}
		return s.add(24)
	case reflect.Slice:
		if value.IsNil() {
			return s.add(4)
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			// encoding/json base64-encodes byte slices; include that expansion.
			length := int64(value.Len())
			if length > (math.MaxInt64-2)/4*3 {
				return errModelOutputEstimateExceeded
			}
			return s.add(((length + 2) / 3) * 4)
		}
		return s.withActive(value, func() error { return s.addJSONSequence(value) })
	case reflect.Array:
		return s.addJSONSequence(value)
	case reflect.Map:
		if value.IsNil() {
			return s.add(4)
		}
		if value.Type().Key().Kind() != reflect.String {
			return errUnsafeModelOutputValue
		}
		return s.withActive(value, func() error {
			if err := s.add(2); err != nil {
				return err
			}
			iterator := value.MapRange()
			for iterator.Next() {
				if err := s.addString(iterator.Key().String()); err != nil {
					return err
				}
				if err := s.add(3); err != nil {
					return err
				}
				if err := s.addJSONValue(iterator.Value()); err != nil {
					return err
				}
			}
			return nil
		})
	case reflect.Struct:
		if err := s.add(2); err != nil {
			return err
		}
		typeInfo := value.Type()
		for i := 0; i < value.NumField(); i++ {
			fieldInfo := typeInfo.Field(i)
			fieldName := strings.Split(fieldInfo.Tag.Get("json"), ",")[0]
			if !fieldInfo.IsExported() || fieldName == "-" {
				continue
			}
			if fieldName == "" {
				fieldName = fieldInfo.Name
			}
			if err := s.addString(fieldName); err != nil {
				return err
			}
			if err := s.addJSONValue(value.Field(i)); err != nil {
				return err
			}
		}
		return nil
	default:
		return errUnsafeModelOutputValue
	}
}

func (s *outputSizeEstimator) addJSONSequence(value reflect.Value) error {
	if err := s.add(2); err != nil {
		return err
	}
	for i := 0; i < value.Len(); i++ {
		if err := s.add(1); err != nil {
			return err
		}
		if err := s.addJSONValue(value.Index(i)); err != nil {
			return err
		}
	}
	return nil
}

func (s *outputSizeEstimator) withActive(value reflect.Value, visit func() error) error {
	key := outputVisit{kind: value.Kind(), ptr: value.Pointer()}
	if key.ptr == 0 {
		return visit()
	}
	if _, exists := s.active[key]; exists {
		return errUnsafeModelOutputValue
	}
	s.active[key] = struct{}{}
	defer delete(s.active, key)
	return visit()
}
