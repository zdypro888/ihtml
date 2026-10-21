// Package providerstream decodes bounded provider SSE into terminal JSON envelopes.
// It owns no HTTP client, credentials, session, tool execution or authorization.
// Hosts may use it without importing an agent framework; agent/eino already owns
// its own model/SDK streaming and should not layer this decoder over that path.
package providerstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

var ErrProtocol = errors.New("invalid provider stream")
var ErrProvider = errors.New("provider stream failed")

// Options contains display/validation hooks, never an authority source.
// Validate runs before every provider event or visible snapshot is exposed.
// OnDelta receives cumulative answer text only (no reasoning/tool arguments).
// Errors from either hook stop decoding and are returned unchanged. The caller
// owns closing/cancelling the reader on return. Hooks run synchronously.
type Options struct {
	Validate    func([]byte) error
	OnDelta     func(string) error
	MinInterval time.Duration
}

// Decode supports responses, chat (Completions), and messages (Anthropic).
// It requires a successful terminal marker and returns the native JSON envelope
// for the host's final semantic/tool validation; partial text is never success.
// Hard bounds: 512KiB SSE, 256KiB event/line, 24KB text, 6 chat calls,
// 20 message blocks and 4KiB streamed arguments. It never invokes a tool.
func Decode(ctx context.Context, protocol string, body io.Reader, options Options) ([]byte, error) {
	if protocol != "responses" && protocol != "chat" && protocol != "messages" {
		return nil, ErrProtocol
	}
	if body == nil {
		return nil, ErrProtocol
	}
	validate := func(raw []byte) error {
		if options.Validate != nil {
			return options.Validate(raw)
		}
		return nil
	}
	scanner := bufio.NewScanner(io.LimitReader(body, 512*1024+1))
	scanner.Buffer(make([]byte, 4096), 256*1024)
	events := &eventScanner{scanner: scanner}
	text := ""
	published := ""
	last := time.Time{}
	done := false
	var final json.RawMessage
	type call struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	calls := map[int]*call{}
	blocks := map[int]map[string]any{}
	finish := ""
	publish := func(force bool) error {
		if len(text) > 24000 {
			return ErrProtocol
		}
		if text == published || !force && time.Since(last) < options.MinInterval {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validate(marshal(text)); err != nil {
			return err
		}
		if options.OnDelta != nil {
			if err := options.OnDelta(text); err != nil {
				return err
			}
		}
		published = text
		last = time.Now()
		return nil
	}
	for events.next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw := events.data
		if raw == "[DONE]" {
			if protocol != "chat" {
				return nil, ErrProtocol
			}
			done = true
			break
		}
		if err := validate([]byte(raw)); err != nil {
			return nil, err
		}
		var event struct {
			Type     string          `json:"type"`
			Delta    json.RawMessage `json:"delta"`
			Response json.RawMessage `json:"response"`
			Index    int             `json:"index"`
			Block    map[string]any  `json:"content_block"`
			Choices  []struct {
				Index  int    `json:"index"`
				Finish string `json:"finish_reason"`
				Delta  struct {
					Content string `json:"content"`
					Calls   []struct {
						Index    int `json:"index"`
						ID, Type string
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(raw), &event) != nil {
			return nil, ErrProtocol
		}
		switch protocol {
		case "chat":
			if len(event.Choices) > 1 {
				return nil, ErrProtocol
			}
			for _, choice := range event.Choices {
				if choice.Index != 0 {
					return nil, ErrProtocol
				}
				text += choice.Delta.Content
				if choice.Finish != "" {
					finish = choice.Finish
				}
				for _, part := range choice.Delta.Calls {
					if part.Index < 0 || part.Index >= 6 {
						return nil, ErrProtocol
					}
					c := calls[part.Index]
					if c == nil {
						c = &call{}
						calls[part.Index] = c
					}
					c.ID += part.ID
					c.Type += part.Type
					c.Function.Name += part.Function.Name
					c.Function.Arguments += part.Function.Arguments
					if len(c.ID) > 256 || len(c.Function.Name) > 120 || len(c.Function.Arguments) > 4096 {
						return nil, ErrProtocol
					}
				}
			}
		case "messages":
			switch event.Type {
			case "content_block_start":
				if event.Index < 0 || event.Index >= 20 || blocks[event.Index] != nil {
					return nil, ErrProtocol
				}
				if event.Block == nil {
					return nil, ErrProtocol
				}
				blocks[event.Index] = event.Block
				if event.Block["type"] == "text" {
					v, _ := event.Block["text"].(string)
					text += v
				}
			case "content_block_delta":
				b := blocks[event.Index]
				if b == nil {
					return nil, ErrProtocol
				}
				var d struct {
					Type, Text string
					Partial    string `json:"partial_json"`
				}
				if json.Unmarshal(event.Delta, &d) != nil {
					return nil, ErrProtocol
				}
				if d.Type == "text_delta" {
					v, _ := b["text"].(string)
					b["text"] = v + d.Text
					text += d.Text
				} else if d.Type == "input_json_delta" {
					v, _ := b["partial"].(string)
					v += d.Partial
					if len(v) > 4096 {
						return nil, ErrProtocol
					}
					b["partial"] = v
				} else {
					return nil, ErrProtocol
				}
			case "message_delta":
				var d struct {
					Stop string `json:"stop_reason"`
				}
				if json.Unmarshal(event.Delta, &d) != nil {
					return nil, ErrProtocol
				}
				finish = d.Stop
			case "message_stop":
				done = true
			case "error":
				return nil, ErrProvider
			}
		default:
			switch event.Type {
			case "response.output_text.delta":
				var d string
				if json.Unmarshal(event.Delta, &d) != nil {
					return nil, ErrProtocol
				}
				text += d
			case "response.completed":
				final = event.Response
				done = true
			case "response.failed", "response.incomplete", "error":
				return nil, ErrProvider
			}
		}
		if err := publish(false); err != nil {
			return nil, err
		}
		if done {
			break
		}
	}
	if events.err != nil || !done {
		return nil, ErrProtocol
	}
	if err := publish(true); err != nil {
		return nil, err
	}
	if protocol == "chat" {
		cc := []*call{}
		for i := 0; i < len(calls); i++ {
			if calls[i] == nil {
				return nil, ErrProtocol
			}
			cc = append(cc, calls[i])
		}
		return marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{"role": "assistant", "content": text, "tool_calls": cc}}}}), nil
	}
	if protocol == "messages" {
		content := []any{}
		for i := 0; i < len(blocks); i++ {
			b := blocks[i]
			if b == nil {
				return nil, ErrProtocol
			}
			if v, ok := b["partial"].(string); ok {
				var input any
				if json.Unmarshal([]byte(v), &input) != nil {
					return nil, ErrProtocol
				}
				b["input"] = input
				delete(b, "partial")
			}
			content = append(content, b)
		}
		return marshal(map[string]any{"type": "message", "stop_reason": finish, "content": content}), nil
	}
	if len(final) == 0 {
		return nil, ErrProtocol
	}
	return final, nil
}

func marshal(v any) []byte { b, _ := json.Marshal(v); return b }

// SSE data belongs to an event, not a line. Join multiple data fields with LF;
// comments, event names and CRLF do not change JSON or force a premature flush.
type eventScanner struct {
	scanner *bufio.Scanner
	data    string
	total   int
	err     error
}

func (s *eventScanner) next() bool {
	parts := []string{}
	size := 0
	for s.scanner.Scan() {
		line := s.scanner.Text()
		s.total += len(line) + 1
		if s.total > 512*1024 {
			s.err = ErrProtocol
			return false
		}
		if line == "" {
			if len(parts) > 0 {
				s.data = strings.Join(parts, "\n")
				return true
			}
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		value := strings.TrimPrefix(line, "data:")
		value = strings.TrimPrefix(value, " ")
		size += len(value) + 1
		if size > 256*1024 {
			s.err = ErrProtocol
			return false
		}
		parts = append(parts, value)
	}
	s.err = s.scanner.Err()
	// An event must end at a blank line, including the terminal event. Otherwise
	// network truncation could masquerade as completion.
	if len(parts) > 0 && s.err == nil {
		s.err = ErrProtocol
	}
	return false
}
