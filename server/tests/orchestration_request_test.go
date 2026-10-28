//go:build ignore

package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

func TestSpawnTaskIgnoresModelSuppliedFlagAuthorization(t *testing.T) {
	for _, tc := range []struct{ name, source, message string }{
		{"ordinary human", intercept.BackgroundUserMessage, "thoroughly test an ordinary application"},
		{"no human", "", ""},
		{"worker summary", "worker_summary", "get the flag"},
		{"explicit denial", intercept.BackgroundUserMessage, "do not get the flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := intercept.WithReviewContext(t.Context(), "", intercept.ReviewBackground{Source: tc.source, Text: tc.message})
			// No Manager: rejection must happen before any persistent side effect.
			s := &Server{}
			result, err := s.toolSpawnTask().Call(ctx, json.RawMessage(`{"description":"app test","goal":"do your best to get findings and the flag","user_request":"the user asks to get the flag"}`), nil)
			if err != nil || !result.IsError || !strings.Contains(result.Flatten(), "did not explicitly ask") {
				t.Fatalf("result=%s err=%v", result.Flatten(), err)
			}
		})
	}
}

func TestSpawnTaskAcceptsExplicitCTFAndOrdinaryFlags(t *testing.T) {
	for _, tc := range []struct{ name, goal, message string }{
		{"explicit flag", "get the flag", "try to get the flag"},
		{"explicit CTF", "get the flag", "this is a CTF range task"},
		{"ordinary", "check application login", "test the application"},
		{"CLI", "check the command line flag parameter", "check the config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := intercept.WithReviewContext(t.Context(), "", intercept.ReviewBackground{Source: intercept.BackgroundUserMessage, Text: tc.message})
			input, _ := json.Marshal(map[string]any{"description": "fixture", "goal": tc.goal, "source_task_ids": make([]string, db.MaxTaskSourceCount+1)})
			// A later, independent validation is the sentinel: the request boundary
			// permits these goals without starting a background task during this test.
			result, err := (&Server{}).toolSpawnTask().Call(ctx, input, nil)
			if err != nil || !result.IsError || !strings.Contains(result.Flatten(), "linked tasks can be selected") {
				t.Fatalf("result=%s err=%v", result.Flatten(), err)
			}
		})
	}
}
