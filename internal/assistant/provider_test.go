package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestStreamRedactsKeysAcrossChunks(t *testing.T) {
	secret := "fake-gateway-private-value"
	raw := ""
	for _, text := range []string{"测试 ", "fake-gateway-", "private-value", " 已隐藏"} {
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": text}}}})
		raw += fmt.Sprintf("data: %s\n\n", body)
	}
	raw += "data: [DONE]\n\n"
	var text strings.Builder
	reply, err := readStream(context.Background(), strings.NewReader(raw), "openai", secret, func(event string, value any) error {
		if event == "text" {
			text.WriteString(value.(map[string]string)["text"])
		}
		return nil
	})
	if err != nil || strings.Contains(reply.Content, secret) || strings.Contains(text.String(), secret) || !strings.Contains(text.String(), "[密钥已隐藏]") {
		t.Fatalf("redaction failed: error=%v", err)
	}
}

func TestAnthropicStreamAndToolResultShape(t *testing.T) {
	raw := `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"查询资产"}}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-1","name":"assets_upcoming","input":{}}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"days\":30}"}}

data: {"type":"message_stop"}

`
	reply, err := readStream(context.Background(), strings.NewReader(raw), "anthropic", "", func(string, any) error { return nil })
	if err != nil || reply.Content != "查询资产" || len(reply.Calls) != 1 || string(reply.Calls[0].Arguments) != `{"days":30}` {
		t.Fatalf("Anthropic stream: %+v %v", reply, err)
	}
	history := []Message{{Role: "user", Content: "查询"}, reply, {Role: "tool", CallID: "call-1", Content: `{"items":[]}`}}
	messages := anthropicMessages(history)
	last := messages[len(messages)-1].(map[string]any)
	block := last["content"].([]any)[0].(map[string]any)
	if last["role"] != "user" || block["type"] != "tool_result" || block["tool_use_id"] != "call-1" {
		t.Fatalf("tool_result shape: %+v", last)
	}
}

func TestIncompleteStreamNeverExecutesPartialTool(t *testing.T) {
	raw := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"partial\",\"function\":{\"name\":\"assets_delete\",\"arguments\":\"{\\\"id\\\":\"}}]}}]}\n\n"
	if _, err := readStream(context.Background(), strings.NewReader(raw), "openai", "", func(string, any) error { return nil }); err == nil {
		t.Fatal("an interrupted stream was accepted")
	}
}
