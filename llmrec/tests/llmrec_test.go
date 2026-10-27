//go:build ignore

package llmrec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
	"github.com/google/uuid"
)

type completeProvider struct{}

func (completeProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(func(llm.StreamEvent, error) bool) {}
}

func (completeProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			{Type: llm.BlockThinking, Thinking: "reasoning"},
			llm.TextBlock("answer"),
		},
	}, "stop", llm.Usage{InputTokens: 7, OutputTokens: 3}, nil
}

func TestTaskIDContextUsesExplicitRegistryID(t *testing.T) {
	ctx := WithTaskID(context.Background(), " 42 ")
	if got := TaskIDFrom(ctx); got != "42" {
		t.Fatalf("TaskIDFrom()=%q want 42", got)
	}
	if got := TaskIDFrom(WithTaskID(ctx, "   ")); got != "42" {
		t.Fatalf("blank task id should preserve parent context, got %q", got)
	}
	if got := TaskIDFrom(nil); got != "" {
		t.Fatalf("nil context returned %q", got)
	}
}

func TestParseSessionFallbackIsExplorationScoped(t *testing.T) {
	taskID, worker := parseSession("exp12-worker-i99")
	if taskID != "12" || worker != "worker" {
		t.Fatalf("parseSession()=(%q,%q)", taskID, worker)
	}
	if taskID, worker := parseSession("not-a-task"); taskID != "" || worker != "" {
		t.Fatalf("unexpected non-session parse: (%q,%q)", taskID, worker)
	}
}

func TestCompleteForwardsAtomicResponse(t *testing.T) {
	recorder := Wrap(completeProvider{}, nil, "model", "profile", "", "", func() bool { return false })
	msg, stopReason, usage, err := recorder.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got := msg.Text(); got != "answer" {
		t.Fatalf("Complete() text=%q want answer", got)
	}
	if stopReason != "stop" {
		t.Fatalf("Complete() stop reason=%q want stop", stopReason)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 3 {
		t.Fatalf("Complete() usage=%+v", usage)
	}
}

type meteredStreamProvider struct{ completeProvider }

func (meteredStreamProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 23}}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: llm.Usage{OutputTokens: 5}}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func TestSideUsageRecordedOnceOnConsumerCancellation(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	for _, early := range []bool{false, true} {
		profile := "btw-metering-" + uuid.NewString()
		ctx, cancel := context.WithCancel(transcript.WithSessionID(t.Context(), "exp0-btw-test"))
		recorder := Wrap(meteredStreamProvider{}, pg, "fixture", profile, "", "", func() bool { return false })
		for event, err := range recorder.Stream(ctx, llm.CompletionRequest{}) {
			if err != nil {
				t.Fatal(err)
			}
			if early && event.Type == llm.SEMessageDelta {
				cancel()
				break // Consumer exits before the provider can return a cancellation event.
			}
		}
		cancel()
		var count, input, output int
		var worker, status string
		err = pg.QueryRow(`SELECT count(*),max(input_tokens),max(output_tokens),max(worker),max(status)
FROM llm_usage WHERE profile_name=$1`, profile).Scan(&count, &input, &output, &worker, &status)
		_, _ = pg.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, profile)
		if err != nil {
			t.Fatal(err)
		}
		wantStatus := "ok"
		if early {
			wantStatus = "error"
		}
		if count != 1 || input != 23 || output != 5 || worker != "btw" || status != wantStatus {
			t.Fatalf("early=%v: count=%d usage=%d/%d worker=%q status=%q", early, count, input, output, worker, status)
		}
	}
}

func TestAnthropicStreamUsageRecordedOnce(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		start       string
		deltas      []string
		interrupted bool
		want        llm.Usage
	}{
		{
			name:  "gateway finalizes cache",
			start: `{"input_tokens":14085,"cache_read_input_tokens":0,"output_tokens":0}`,
			deltas: []string{
				`{"input_tokens":1797,"cache_read_input_tokens":12288,"output_tokens":169}`,
			},
			want: llm.Usage{InputTokens: 14085, OutputTokens: 169, CacheReadTokens: 12288},
		},
		{
			name:  "corrected input and repeated snapshots",
			start: `{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":20,"output_tokens":1}`,
			deltas: []string{
				`{"input_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":30,"output_tokens":4}`,
				`{"output_tokens":9}`,
				`{"output_tokens":9}`,
			},
			want: llm.Usage{InputTokens: 120, OutputTokens: 9, CacheReadTokens: 80, CacheWriteTokens: 30},
		},
		{
			name:        "interrupted stream keeps initial input",
			start:       `{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":20,"output_tokens":1}`,
			interrupted: true,
			want:        llm.Usage{InputTokens: 160, CacheReadTokens: 40, CacheWriteTokens: 20},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := "anthropic-usage-" + uuid.NewString()
			t.Cleanup(func() {
				if _, err := pg.ExecContext(context.Background(), `DELETE FROM llm_records WHERE profile_name=$1`, profile); err != nil {
					t.Errorf("clean records: %v", err)
				}
				if _, err := pg.ExecContext(context.Background(), `DELETE FROM llm_usage WHERE profile_name=$1`, profile); err != nil {
					t.Errorf("clean usage: %v", err)
				}
			})
			frames := []string{`{"type":"message_start","message":{"usage":` + tt.start + `}}`}
			for _, usage := range tt.deltas {
				frames = append(frames, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":`+usage+`}`)
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
			provider, err := llm.NewProvider(llm.Config{
				Format: llm.FormatAnthropic, BaseURL: srv.URL, Model: "fixture", APIKey: "test-key",
			})
			if err != nil {
				t.Fatal(err)
			}
			recorder := Wrap(provider, pg, "fixture", profile, "", "", nil)
			ctx := transcript.WithSessionID(t.Context(), "exp0-mainagent")
			var streamErr error
			for _, err := range recorder.Stream(ctx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("hello")}}) {
				if err != nil {
					streamErr = err
					break
				}
			}
			wantStatus := "ok"
			if tt.interrupted {
				wantStatus = "error"
				if !errors.Is(streamErr, io.ErrUnexpectedEOF) {
					t.Fatalf("stream error = %v, want unexpected EOF", streamErr)
				}
			} else if streamErr != nil {
				t.Fatal(streamErr)
			}

			// Assert the durable ledger and normalized trace agree after one real
			// provider call, including its error path and duplicate usage frames.
			var count int
			var usage llm.Usage
			var status, response string
			err = pg.QueryRowContext(t.Context(), `SELECT count(*),sum(input_tokens),sum(output_tokens),
sum(cache_read),sum(cache_write),max(status) FROM llm_usage WHERE profile_name=$1`, profile).
				Scan(&count, &usage.InputTokens, &usage.OutputTokens, &usage.CacheReadTokens, &usage.CacheWriteTokens, &status)
			if err != nil {
				t.Fatal(err)
			}
			if count != 1 || usage != tt.want || status != wantStatus {
				t.Fatalf("ledger count=%d usage=%+v status=%q, want one row %+v %q", count, usage, status, tt.want, wantStatus)
			}
			err = pg.QueryRowContext(t.Context(), `SELECT count(*),sum(input_tokens),sum(output_tokens),
sum(cache_read),sum(cache_write),max(status),max(response_body) FROM llm_records WHERE profile_name=$1`, profile).
				Scan(&count, &usage.InputTokens, &usage.OutputTokens, &usage.CacheReadTokens, &usage.CacheWriteTokens, &status, &response)
			if err != nil {
				t.Fatal(err)
			}
			if count != 1 || usage != tt.want || status != wantStatus {
				t.Fatalf("trace count=%d usage=%+v status=%q, want one row %+v %q", count, usage, status, tt.want, wantStatus)
			}
			var body struct {
				Usage llm.Usage `json:"usage"`
			}
			if err := json.Unmarshal([]byte(response), &body); err != nil {
				t.Fatal(err)
			}
			if body.Usage != tt.want {
				t.Fatalf("response usage=%+v, want %+v", body.Usage, tt.want)
			}
		})
	}
}
