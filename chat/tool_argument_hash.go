package chat

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash"
	"math"
	"sort"
	"strconv"
	"strings"
)

const maxToolArgumentHashBytes = maxToolArgumentsBytes + (128 << 10)

var errToolArgumentHash = errors.New("tool arguments cannot be safely fingerprinted")

// toolArgumentHashState writes an unambiguous, deterministic representation
// straight into SHA-256. It never materializes a canonical JSON copy (which can
// expand several-fold under HTML escaping), while sorted object keys preserve
// the repeated-call semantics used by the tool budget.
type toolArgumentHashState struct {
	digest hash.Hash
	bytes  int
	nodes  int
	keys   int
	buffer [4096]byte
}

func (s *toolArgumentHashState) account(size int) error {
	if size < 0 || size > maxToolArgumentHashBytes-s.bytes {
		return errToolArgumentHash
	}
	s.bytes += size
	return nil
}

func (s *toolArgumentHashState) writeByte(value byte) error {
	if err := s.account(1); err != nil {
		return err
	}
	s.buffer[0] = value
	_, _ = s.digest.Write(s.buffer[:1])
	return nil
}

func (s *toolArgumentHashState) writeUint64(value uint64) error {
	if err := s.account(8); err != nil {
		return err
	}
	binary.BigEndian.PutUint64(s.buffer[:8], value)
	_, _ = s.digest.Write(s.buffer[:8])
	return nil
}

func (s *toolArgumentHashState) writeString(tag byte, value string) error {
	if err := s.writeByte(tag); err != nil {
		return err
	}
	if err := s.writeUint64(uint64(len(value))); err != nil {
		return err
	}
	if err := s.account(len(value)); err != nil {
		return err
	}
	for len(value) > 0 {
		size := min(len(value), len(s.buffer))
		copy(s.buffer[:size], value[:size])
		_, _ = s.digest.Write(s.buffer[:size])
		value = value[size:]
	}
	return nil
}

func (s *toolArgumentHashState) takeNode(depth int) error {
	if depth > maxToolArgumentDepth || s.nodes >= maxToolArgumentNodes {
		return errToolArgumentHash
	}
	s.nodes++
	return nil
}

func (s *toolArgumentHashState) writeValue(value any, depth int) error {
	if err := s.takeNode(depth); err != nil {
		return err
	}
	switch typed := value.(type) {
	case nil:
		return s.writeByte('n')
	case bool:
		if typed {
			return s.writeByte('t')
		}
		return s.writeByte('f')
	case string:
		return s.writeString('s', typed)
	case float64:
		number, err := stableToolNumber(typed, 64)
		if err != nil {
			return err
		}
		return s.writeString('d', number)
	case float32:
		number, err := stableToolNumber(float64(typed), 32)
		if err != nil {
			return err
		}
		return s.writeString('d', number)
	case int:
		return s.writeString('d', strconv.FormatInt(int64(typed), 10))
	case int8:
		return s.writeString('d', strconv.FormatInt(int64(typed), 10))
	case int16:
		return s.writeString('d', strconv.FormatInt(int64(typed), 10))
	case int32:
		return s.writeString('d', strconv.FormatInt(int64(typed), 10))
	case int64:
		return s.writeString('d', strconv.FormatInt(typed, 10))
	case uint:
		return s.writeString('d', strconv.FormatUint(uint64(typed), 10))
	case uint8:
		return s.writeString('d', strconv.FormatUint(uint64(typed), 10))
	case uint16:
		return s.writeString('d', strconv.FormatUint(uint64(typed), 10))
	case uint32:
		return s.writeString('d', strconv.FormatUint(uint64(typed), 10))
	case uint64:
		return s.writeString('d', strconv.FormatUint(typed, 10))
	case json.Number:
		raw := strings.TrimSpace(string(typed))
		if raw == "" || len(raw) > 128 || !json.Valid([]byte(raw)) {
			return errToolArgumentHash
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return errToolArgumentHash
		}
		if _, ok := decoded.(json.Number); !ok {
			return errToolArgumentHash
		}
		return s.writeString('d', raw)
	case []any:
		if err := s.writeByte('a'); err != nil {
			return err
		}
		if err := s.writeUint64(uint64(len(typed))); err != nil {
			return err
		}
		for _, entry := range typed {
			if err := s.writeValue(entry, depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if len(typed) > maxToolArgumentKeys-s.keys || len(typed) > maxToolArgumentNodes-s.nodes {
			return errToolArgumentHash
		}
		s.keys += len(typed)
		s.nodes += len(typed) // object keys count as structural nodes
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if err := s.writeByte('o'); err != nil {
			return err
		}
		if err := s.writeUint64(uint64(len(keys))); err != nil {
			return err
		}
		for _, key := range keys {
			if err := s.writeString('k', key); err != nil {
				return err
			}
			if err := s.writeValue(typed[key], depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		// No reflection, Stringer, Marshaler, pointers, or host callbacks are
		// accepted. Semantic tool arguments originate from JSON and therefore
		// need only the closed set above; everything else fails closed.
		return errToolArgumentHash
	}
}

func stableToolNumber(value float64, bits int) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", errToolArgumentHash
	}
	if value == 0 { // normalize negative zero
		return "0", nil
	}
	return strconv.FormatFloat(value, 'g', -1, bits), nil
}

func canonicalToolArgumentDigest(name string, args map[string]any) ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	if len(name) == 0 || len(name) > 128 {
		return out, errToolArgumentHash
	}
	state := &toolArgumentHashState{digest: sha256.New()}
	if err := state.writeString('T', name); err != nil {
		return out, err
	}
	if err := state.writeValue(args, 0); err != nil {
		return out, err
	}
	copy(out[:], state.digest.Sum(nil))
	return out, nil
}
