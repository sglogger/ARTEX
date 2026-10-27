//go:build ignore

package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicStreamUsage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		start       string
		deltas      []string
		interrupted bool
		want        Usage
	}{
		{
			name:  "gateway finalizes cache at completion",
			start: `{"input_tokens":14085,"cache_read_input_tokens":0,"output_tokens":0}`,
			deltas: []string{
				`{"input_tokens":1797,"cache_read_input_tokens":12288,"output_tokens":169}`,
			},
			want: Usage{InputTokens: 14085, OutputTokens: 169, CacheReadTokens: 12288},
		},
		{
			name:   "output only completion preserves initial input and cache",
			start:  `{"input_tokens":100,"cache_read_input_tokens":80,"cache_creation_input_tokens":20,"output_tokens":1}`,
			deltas: []string{`{"output_tokens":9}`},
			want:   Usage{InputTokens: 200, OutputTokens: 9, CacheReadTokens: 80, CacheWriteTokens: 20},
		},
		{
			name:  "repeated cumulative snapshots count once",
			start: `{"input_tokens":100,"cache_read_input_tokens":80,"cache_creation_input_tokens":20,"output_tokens":1}`,
			deltas: []string{
				`{"input_tokens":100,"cache_read_input_tokens":80,"cache_creation_input_tokens":20,"output_tokens":4}`,
				`{"input_tokens":100,"cache_read_input_tokens":80,"cache_creation_input_tokens":20,"output_tokens":9}`,
				`{"input_tokens":100,"cache_read_input_tokens":80,"cache_creation_input_tokens":20,"output_tokens":9}`,
			},
			want: Usage{InputTokens: 200, OutputTokens: 9, CacheReadTokens: 80, CacheWriteTokens: 20},
		},
		{
			name:  "final snapshot corrects preliminary input and cache",
			start: `{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":20}`,
			deltas: []string{
				`{"input_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":30,"output_tokens":9}`,
			},
			want: Usage{InputTokens: 120, OutputTokens: 9, CacheReadTokens: 80, CacheWriteTokens: 30},
		},
		{
			name:  "explicit zero replaces preliminary values",
			start: `{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":20}`,
			deltas: []string{
				`{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":9}`,
			},
			want: Usage{OutputTokens: 9},
		},
		{
			name:  "partial correction preserves omitted fields",
			start: `{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":20}`,
			deltas: []string{
				`{"cache_read_input_tokens":80,"output_tokens":9}`,
				`{"input_tokens":10}`,
			},
			want: Usage{InputTokens: 110, OutputTokens: 9, CacheReadTokens: 80, CacheWriteTokens: 20},
		},
		{
			name: "completion without initial usage",
			deltas: []string{
				`{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":20,"output_tokens":9}`,
			},
			want: Usage{InputTokens: 160, OutputTokens: 9, CacheReadTokens: 40, CacheWriteTokens: 20},
		},
		{
			name:   "explicit output correction replaces cumulative output",
			start:  `{"input_tokens":100,"output_tokens":1}`,
			deltas: []string{`{"output_tokens":9}`, `{"output_tokens":0}`},
			want:   Usage{InputTokens: 100},
		},
		{
			name:        "interrupted stream preserves reported input",
			start:       `{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":20,"output_tokens":1}`,
			interrupted: true,
			want:        Usage{InputTokens: 160, CacheReadTokens: 40, CacheWriteTokens: 20},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			frames := []string{`{"type":"message_start","message":{}}`}
			if tt.start != "" {
				frames[0] = `{"type":"message_start","message":{"usage":` + tt.start + `}}`
			}
			for _, usage := range tt.deltas {
				frames = append(frames, `{"type":"message_delta","usage":`+usage+`}`)
			}
			if !tt.interrupted {
				frames = append(frames, `{"type":"message_stop"}`)
			}
			payload := "data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := io.WriteString(w, payload); err != nil {
					t.Errorf("write stream: %v", err)
				}
			}))
			defer srv.Close()
			provider, err := NewProvider(Config{
				Format: FormatAnthropic, Model: "test-model", BaseURL: srv.URL, APIKey: "test-key",
			})
			if err != nil {
				t.Fatal(err)
			}
			// A reused provider must start a fresh metering snapshot for each call.
			for attempt := 0; attempt < 2; attempt++ {
				acc := NewAccumulator()
				var streamErr error
				for ev, err := range provider.Stream(context.Background(), CompletionRequest{
					Messages: []Message{UserText("hello")},
				}) {
					if err != nil {
						streamErr = err
						break
					}
					acc.Add(ev)
				}
				if tt.interrupted {
					if !errors.Is(streamErr, io.ErrUnexpectedEOF) {
						t.Fatalf("stream error = %v, want unexpected EOF", streamErr)
					}
				} else if streamErr != nil {
					t.Fatal(streamErr)
				}
				if acc.Usage != tt.want {
					t.Fatalf("attempt %d usage = %+v, want %+v", attempt, acc.Usage, tt.want)
				}
			}
		})
	}
}
