package chat

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zdypro888/ihtml"
)

const (
	// One tool call must leave room for the confirmation envelope and signature
	// inside the 1 MiB WebSocket inbound frame that the browser sends back.
	maxToolArgumentsBytes       = 768 << 10
	maxToolArgumentDepth        = 32
	maxToolArgumentNodes        = 8192
	maxToolArgumentKeys         = 4096
	maxToolCallIDBytes          = 256
	maxPlanItemIDBytes          = 128
	maxConfirmationFrameSoftCap = 896 << 10
	maxToolResultJSONBytes      = 64 << 10
	maxToolResultStringBytes    = 40 << 10
	maxToolResultNodes          = 2048
	maxToolResultDepth          = 16
	maxToolResultCollectionSize = 128
	maxToolInputPreviewBytes    = 400
	maxToolDebugPreviewBytes    = 8 << 10
	maxToolSummaryBytes         = 2 << 10
	maxToolNameBytes            = 128
	maxToolTitleBytes           = 256
	maxTimelineEvidenceEntries  = 12
	maxTimelineEvidenceBody     = 12 << 10
	maxTimelineEvidenceBytes    = 64 << 10
)

var errToolPayloadLimit = errors.New("tool payload exceeds safety limits")

type jsonTokenFrame struct {
	kind      json.Delim
	expectKey bool
}

// validateToolArgumentsJSON performs structural checks before Unmarshal builds
// maps and slices. This bounds both raw bytes and adversarial nesting/fan-out.
func validateToolArgumentsJSON(raw string) error {
	if len(raw) > maxToolArgumentsBytes {
		return errToolPayloadLimit
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	stack := make([]jsonTokenFrame, 0, 8)
	nodes, keys := 0, 0
	rootStarted, rootComplete := false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if rootComplete {
			return errors.New("tool arguments contain multiple JSON values")
		}
		if len(stack) > 0 && stack[len(stack)-1].kind == '{' && stack[len(stack)-1].expectKey {
			if delim, ok := token.(json.Delim); ok && delim == '}' {
				// handled by the closing-delimiter branch below
			} else {
				if _, ok := token.(string); !ok {
					return errors.New("tool argument object key is invalid")
				}
				keys++
				nodes++
				if keys > maxToolArgumentKeys || nodes > maxToolArgumentNodes {
					return errToolPayloadLimit
				}
				stack[len(stack)-1].expectKey = false
				continue
			}
		}

		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{', '[':
				rootStarted = true
				nodes++
				if nodes > maxToolArgumentNodes || len(stack)+1 > maxToolArgumentDepth {
					return errToolPayloadLimit
				}
				stack = append(stack, jsonTokenFrame{kind: value, expectKey: value == '{'})
			case '}', ']':
				if len(stack) == 0 {
					return errors.New("tool arguments contain an unmatched delimiter")
				}
				open := stack[len(stack)-1].kind
				if (open == '{' && value != '}') || (open == '[' && value != ']') {
					return errors.New("tool arguments contain mismatched delimiters")
				}
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					rootComplete = true
				} else if stack[len(stack)-1].kind == '{' {
					stack[len(stack)-1].expectKey = true
				}
			}
		default:
			rootStarted = true
			nodes++
			if nodes > maxToolArgumentNodes {
				return errToolPayloadLimit
			}
			if len(stack) == 0 {
				rootComplete = true
			} else if stack[len(stack)-1].kind == '{' {
				stack[len(stack)-1].expectKey = true
			}
		}
	}
	if !rootStarted || !rootComplete || len(stack) != 0 {
		return errors.New("tool arguments are incomplete")
	}
	return nil
}

func normalizeToolCallID(raw, toolName string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return toolName + "_" + fmt.Sprint(time.Now().UnixNano())
	}
	if len(raw) <= maxToolCallIDBytes && !strings.ContainsFunc(raw, unicode.IsControl) {
		return raw
	}
	digest := sha256.Sum256([]byte(raw))
	return "tool_" + hex.EncodeToString(digest[:16])
}

func validPlanItemID(id string) bool {
	return id != "" && len(id) <= maxPlanItemIDBytes && !strings.ContainsFunc(id, unicode.IsControl)
}

func clippedUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	const suffix = "…"
	if maxBytes <= len(suffix) {
		return clipRunes(value, maxBytes)
	}
	return clipRunes(value, maxBytes-len(suffix)) + suffix
}

// jsonEncodedStringSize mirrors encoding/json's default string escaping
// without allocating an encoded copy. It includes the surrounding quotes and
// fails as soon as maxBytes would be crossed.
func jsonEncodedStringSize(value string, maxBytes int) (int, bool) {
	if maxBytes < 2 {
		return 0, false
	}
	size := 2
	add := func(amount int) bool {
		if amount < 0 || amount > maxBytes-size {
			return false
		}
		size += amount
		return true
	}
	for index := 0; index < len(value); {
		char := value[index]
		if char < utf8.RuneSelf {
			amount := 1
			switch char {
			case '\\', '"', '\b', '\f', '\n', '\r', '\t':
				amount = 2
			default:
				if char < 0x20 || char == '<' || char == '>' || char == '&' {
					amount = 6
				}
			}
			if !add(amount) {
				return 0, false
			}
			index++
			continue
		}
		r, width := utf8.DecodeRuneInString(value[index:])
		amount := width
		if r == utf8.RuneError && width == 1 {
			amount = 6 // conservative replacement-rune encoding
		} else if r == '\u2028' || r == '\u2029' {
			amount = 6
		}
		if !add(amount) {
			return 0, false
		}
		index += width
	}
	return size, true
}

func boundedToolName(value string) string {
	return clippedUTF8(strings.TrimSpace(value), maxToolNameBytes)
}

func boundedToolTitle(value string) string {
	return clippedUTF8(strings.TrimSpace(value), maxToolTitleBytes)
}

func boundedToolSummary(value string) string {
	return clippedUTF8(strings.TrimSpace(value), maxToolSummaryBytes)
}

// toolEventCallReference keeps only correlation and display metadata. Running
// and result events must not echo a potentially large/sensitive argument body
// or confirmation token that the browser already holds.
func toolEventCallReference(call *ihtml.ProposedToolCall) *ihtml.ProposedToolCall {
	if call == nil {
		return nil
	}
	// Build an allowlisted reference instead of copying the whole card. Besides
	// Body/Query/token, Reason and arbitrary display strings are unnecessary on
	// running/result events and could otherwise amplify a forged inbound card.
	return &ihtml.ProposedToolCall{
		ID:                   clipRunes(call.ID, min(len(call.ID), maxToolCallIDBytes)),
		Name:                 boundedToolName(call.Name),
		Title:                boundedToolTitle(call.Title),
		Method:               clipRunes(call.Method, min(len(call.Method), 32)),
		Path:                 clipRunes(call.Path, min(len(call.Path), 512)),
		Risk:                 clipRunes(call.Risk, min(len(call.Risk), 32)),
		RequiresConfirmation: call.RequiresConfirmation,
		AgentMode:            clipRunes(call.AgentMode, min(len(call.AgentMode), 32)),
		ApprovalResume:       clipRunes(call.ApprovalResume, min(len(call.ApprovalResume), 64)),
		ConfirmationNonce:    clipRunes(call.ConfirmationNonce, min(len(call.ConfirmationNonce), 128)),
		ConfirmationExpires:  call.ConfirmationExpires,
	}
}

func boundedToolStreamEvent(event ihtml.ChatStreamEvent) ihtml.ChatStreamEvent {
	event.ToolName = boundedToolName(event.ToolName)
	event.ToolTitle = boundedToolTitle(event.ToolTitle)
	event.ToolSummary = boundedToolSummary(event.ToolSummary)
	event.ToolInput = clippedUTF8(event.ToolInput, maxToolInputPreviewBytes)
	if len(event.ToolDebug) > 0 && (len(event.ToolDebug) > maxToolDebugPreviewBytes || !json.Valid(event.ToolDebug)) {
		event.ToolDebug = boundedToolEventResult(event.ToolDebug, maxToolDebugPreviewBytes)
	}
	if len(event.ToolResult) > 0 && (len(event.ToolResult) > 32000 || !json.Valid(event.ToolResult)) {
		event.ToolResult = boundedToolEventResult(event.ToolResult, 32000)
	}
	if event.Type == ihtml.ChatStreamToolStart || event.Type == ihtml.ChatStreamToolResult {
		event.ToolCall = toolEventCallReference(event.ToolCall)
	}
	return event
}

func confirmationFrameByteLimit() int {
	limit := ihtml.MaxChatOutboundFrameBytes - (4 << 10)
	if limit <= 0 {
		return 0
	}
	if limit > maxConfirmationFrameSoftCap {
		return maxConfirmationFrameSoftCap
	}
	return limit
}

func confirmationFrameFits(call ihtml.ProposedToolCall, toolInput string) bool {
	candidate := call
	if candidate.ConfirmationNonce == "" {
		candidate.ConfirmationNonce = strings.Repeat("0", 32)
	}
	if candidate.ConfirmationToken == "" {
		candidate.ConfirmationToken = strings.Repeat("0", 64)
	}
	if candidate.ConfirmationExpires == 0 {
		candidate.ConfirmationExpires = time.Now().Add(time.Hour).Unix()
	}
	event := ihtml.ChatStreamEvent{
		Type: ihtml.ChatStreamConfirmationRequired, RunID: strings.Repeat("r", 96),
		TurnID: strings.Repeat("t", 96), Seq: 1, ModelReady: true,
		ToolCall: &candidate, ToolName: candidate.Name, ToolTitle: candidate.Title,
		ToolStatus: ihtml.ChatToolPending, ToolInput: toolInput,
		ToolSummary: "需要用户确认后执行。",
	}
	encoded, err := json.Marshal(event)
	limit := confirmationFrameByteLimit()
	return err == nil && limit > 0 && len(encoded) < limit
}

func confirmationCardStructurallySafe(call *ihtml.ProposedToolCall) bool {
	if call == nil {
		return false
	}
	id, nonce := strings.TrimSpace(call.ID), strings.TrimSpace(call.ConfirmationNonce)
	if id == "" && nonce == "" {
		return false
	}
	if len(id) > maxToolCallIDBytes || strings.ContainsFunc(id, unicode.IsControl) ||
		len(nonce) > 128 || strings.ContainsFunc(nonce, unicode.IsControl) ||
		len(call.ConfirmationToken) > 256 || strings.ContainsFunc(call.ConfirmationToken, unicode.IsControl) {
		return false
	}
	if !jsonPayloadWithinLimits(call.Body, maxToolArgumentsBytes, maxToolArgumentNodes, maxToolArgumentDepth) ||
		!jsonPayloadWithinLimits(call.Query, maxToolArgumentsBytes, maxToolArgumentNodes, maxToolArgumentDepth) {
		return false
	}
	return confirmationFrameFits(*call, "")
}

type boundedValueState struct {
	remaining     int
	nodes         int
	maxNodes      int
	maxDepth      int
	maxCollection int
	truncated     bool
	seen          map[uintptr]bool
}

var (
	rawMessageType    = reflect.TypeOf(json.RawMessage{})
	timeType          = reflect.TypeOf(time.Time{})
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

func (s *boundedValueState) marker() any {
	s.truncated = true
	return "…(truncated)"
}

func (s *boundedValueState) takeString(value string) string {
	if s.remaining <= 0 {
		s.truncated = true
		return "…"
	}
	if len(value) <= s.remaining {
		s.remaining -= len(value)
		return value
	}
	out := clipRunes(value, s.remaining)
	s.remaining = 0
	s.truncated = true
	return out + "…"
}

func (s *boundedValueState) value(v reflect.Value, depth int) any {
	if !v.IsValid() {
		return nil
	}
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if depth > s.maxDepth || s.nodes >= s.maxNodes || s.remaining <= 0 {
		return s.marker()
	}
	s.nodes++
	if v.Type() == timeType {
		return s.takeString(v.Interface().(time.Time).Format(time.RFC3339Nano))
	}
	if v.Type() == rawMessageType {
		raw := v.Interface().(json.RawMessage)
		if len(raw) > s.remaining {
			return s.takeString(string(raw))
		}
		var decoded any
		if json.Unmarshal(raw, &decoded) != nil {
			return s.takeString(string(raw))
		}
		return s.value(reflect.ValueOf(decoded), depth+1)
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		ptr := v.Pointer()
		if s.seen[ptr] {
			return s.marker()
		}
		s.seen[ptr] = true
		defer delete(s.seen, ptr)
		return s.value(v.Elem(), depth+1)
	case reflect.String:
		return s.takeString(v.String())
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		limit := v.Len()
		if limit > s.maxCollection {
			limit = s.maxCollection
			s.truncated = true
		}
		out := make([]any, 0, limit+1)
		for index := 0; index < limit && s.nodes < s.maxNodes && s.remaining > 0; index++ {
			out = append(out, s.value(v.Index(index), depth+1))
		}
		if limit < v.Len() {
			out = append(out, "…(more entries)")
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		out := make(map[string]any)
		iterator := v.MapRange()
		count := 0
		for iterator.Next() {
			if count >= s.maxCollection || s.nodes >= s.maxNodes || s.remaining <= 0 {
				s.truncated = true
				break
			}
			key, ok := plainJSONMapKey(iterator.Key())
			if !ok {
				s.truncated = true
				continue
			}
			key = s.takeString(key)
			out[key] = s.value(iterator.Value(), depth+1)
			count++
		}
		if s.truncated {
			out["_ihtml_truncated"] = true
		}
		return out
	case reflect.Struct:
		out := make(map[string]any)
		for index := 0; index < v.NumField(); index++ {
			fieldInfo := v.Type().Field(index)
			if fieldInfo.PkgPath != "" {
				continue
			}
			name := fieldInfo.Name
			tag := strings.Split(fieldInfo.Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			if tag != "" {
				name = tag
			}
			if name == "" || s.nodes >= s.maxNodes || s.remaining <= 0 {
				s.truncated = true
				break
			}
			out[s.takeString(name)] = s.value(v.Field(index), depth+1)
		}
		if s.truncated {
			out["_ihtml_truncated"] = true
		}
		return out
	default:
		return s.marker()
	}
}

// plainJSONMapKey deliberately avoids fmt.Stringer and encoding.TextMarshaler:
// tool results and external stores are host extension points, so projecting a
// value must never execute attacker-controlled formatting callbacks.
func plainJSONMapKey(v reflect.Value) (string, bool) {
	for v.IsValid() && v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return "", false
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), true
	default:
		return "", false
	}
}

func boundedJSONValue(value any, stringBudget, nodeBudget, depth, collection int) (any, bool) {
	state := &boundedValueState{
		remaining: stringBudget, maxNodes: nodeBudget, maxDepth: depth,
		maxCollection: collection, seen: make(map[uintptr]bool),
	}
	bounded := state.value(reflect.ValueOf(value), 0)
	return bounded, state.truncated
}

func boundedJSONPreview(value any, maxBytes int) string {
	bounded, _ := boundedJSONValue(value, maxBytes, 128, 8, 24)
	raw, err := json.Marshal(bounded)
	if err != nil {
		return ""
	}
	if len(raw) > maxBytes {
		limit := maxBytes - len("…")
		if limit < 0 {
			limit = 0
		}
		return clipRunes(string(raw), limit) + "…"
	}
	return string(raw)
}

func marshalBoundedToolResult(result any) (raw json.RawMessage, truncated bool, err error) {
	defer func() {
		if recover() != nil {
			raw = nil
			truncated = false
			err = errors.New("tool result projection failed")
		}
	}()
	bounded, truncated := boundedJSONValue(
		result, maxToolResultStringBytes, maxToolResultNodes,
		maxToolResultDepth, maxToolResultCollectionSize,
	)
	if truncated {
		if object, ok := bounded.(map[string]any); ok {
			object["_ihtml_truncated"] = true
		} else {
			bounded = map[string]any{"value": bounded, "_ihtml_truncated": true}
		}
	}
	raw, err = json.Marshal(bounded)
	if err != nil {
		return nil, false, err
	}
	if len(raw) > maxToolResultJSONBytes {
		truncated = true
		raw = boundedToolEventResult(raw, maxToolResultJSONBytes)
		if len(raw) == 0 {
			err = errors.New("tool result exceeds encoded safety limit")
		}
	}
	return raw, truncated, err
}

// boundedToolEventResult enforces the limit on the final encoded JSON, not on
// a pre-escaping substring. This matters for quotes/control characters where
// json.Marshal can expand a preview by several times.
func boundedToolEventResult(raw json.RawMessage, maxBytes int) json.RawMessage {
	if maxBytes <= 0 {
		return nil
	}
	if len(raw) <= maxBytes && json.Valid(raw) {
		return append(json.RawMessage(nil), raw...)
	}
	encode := func(limit int) json.RawMessage {
		preview := clipRunes(string(raw), limit)
		out, err := json.Marshal(map[string]any{
			"_ihtml_truncated": true,
			"preview":          preview,
		})
		if err != nil {
			return nil
		}
		return out
	}
	// Binary-search the largest rune-safe preview whose *encoded* envelope
	// fits. Duplicate rune-boundary candidates are harmless.
	low, high := 0, len(raw)
	var best json.RawMessage
	for low <= high {
		mid := low + (high-low)/2
		candidate := encode(mid)
		if len(candidate) <= maxBytes {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if len(best) > 0 {
		return best
	}
	fallback := json.RawMessage(`{"_ihtml_truncated":true}`)
	if len(fallback) <= maxBytes {
		return fallback
	}
	return nil
}

// jsonPayloadWithinLimits estimates a host payload without first marshaling it.
// It is used where the exact value must remain unchanged (SessionStore), unlike
// tool results where a bounded projection is acceptable.
func jsonPayloadWithinLimits(value any, maxBytes, maxNodes, maxDepth int) bool {
	remaining := maxBytes
	nodes := 0
	seen := make(map[uintptr]bool)
	var walk func(reflect.Value, int) bool
	walk = func(v reflect.Value, depth int) bool {
		if !v.IsValid() {
			remaining -= 4
			return remaining >= 0
		}
		for v.Kind() == reflect.Interface {
			if v.IsNil() {
				remaining -= 4
				return remaining >= 0
			}
			v = v.Elem()
		}
		if depth > maxDepth || nodes >= maxNodes || remaining < 0 {
			return false
		}
		nodes++
		if v.Type() == rawMessageType {
			remaining -= v.Len()
			return remaining >= 0 && json.Valid(v.Bytes())
		}
		if v.Type() == timeType {
			remaining -= len(time.RFC3339Nano) + 2
			return remaining >= 0
		}
		// SessionEvent.Message.Extra can contain arbitrary host values. Reject
		// custom encoders without invoking them; otherwise a tiny-looking value
		// could generate an unbounded payload or panic inside the store.
		if v.Type().Implements(jsonMarshalerType) || v.Type().Implements(textMarshalerType) ||
			(v.CanAddr() && (v.Addr().Type().Implements(jsonMarshalerType) || v.Addr().Type().Implements(textMarshalerType))) {
			return false
		}
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				remaining -= 4
				return remaining >= 0
			}
			ptr := v.Pointer()
			if seen[ptr] {
				return false
			}
			seen[ptr] = true
			ok := walk(v.Elem(), depth+1)
			delete(seen, ptr)
			return ok
		case reflect.String:
			encoded, ok := jsonEncodedStringSize(v.String(), remaining)
			if !ok {
				return false
			}
			remaining -= encoded
			return remaining >= 0
		case reflect.Bool:
			remaining -= 5
			return remaining >= 0
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			remaining -= 20
			return remaining >= 0
		case reflect.Float32, reflect.Float64:
			if math.IsNaN(v.Float()) || math.IsInf(v.Float(), 0) {
				return false
			}
			remaining -= 32
			return remaining >= 0
		case reflect.Slice, reflect.Array:
			if v.Kind() == reflect.Slice && v.IsNil() {
				remaining -= 4
				return remaining >= 0
			}
			remaining -= 2
			for index := 0; index < v.Len(); index++ {
				remaining--
				if !walk(v.Index(index), depth+1) {
					return false
				}
			}
		case reflect.Map:
			if v.IsNil() {
				remaining -= 4
				return remaining >= 0
			}
			remaining -= 2
			iterator := v.MapRange()
			for iterator.Next() {
				key, ok := plainJSONMapKey(iterator.Key())
				if !ok {
					return false
				}
				encodedKey, fits := jsonEncodedStringSize(key, remaining)
				if !fits {
					return false
				}
				remaining -= encodedKey + 2
				if remaining < 0 || !walk(iterator.Value(), depth+1) {
					return false
				}
			}
		case reflect.Struct:
			remaining -= 2
			for index := 0; index < v.NumField(); index++ {
				field := v.Type().Field(index)
				if field.PkgPath != "" {
					continue
				}
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					name = field.Name
				}
				encodedName, fits := jsonEncodedStringSize(name, remaining)
				if !fits {
					return false
				}
				remaining -= encodedName + 2
				if remaining < 0 || !walk(v.Field(index), depth+1) {
					return false
				}
			}
		case reflect.Invalid:
			return true
		default:
			return false
		}
		return nodes <= maxNodes && remaining >= 0
	}
	return walk(reflect.ValueOf(value), 0)
}
