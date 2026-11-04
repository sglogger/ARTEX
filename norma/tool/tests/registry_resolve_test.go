//go:build ignore

package tool

import "testing"

func TestRegistryResolveToleratesNameDrift(t *testing.T) {
	reg := NewRegistry(NewBash(), NewWebFetch(WebFetchConfig{}), NewTodoStore().Tool(), NewLS())
	for in, want := range map[string]string{
		"Bash":       "Bash",
		"bash":       "Bash",
		"BASH":       "Bash",
		"WebFetch":   "WebFetch",
		"web_fetch":  "WebFetch",
		"web-fetch":  "WebFetch",
		"todo_write": "TodoWrite",
		"ls":         "LS",
	} {
		tl, name, ok := reg.Resolve(in)
		if !ok || name != want || tl.Name() != want {
			t.Fatalf("Resolve(%q) = %v, %q, %v; want %q", in, tl, name, ok, want)
		}
	}
	if _, _, ok := reg.Resolve("bashx"); ok {
		t.Fatal("unrelated name must not resolve")
	}
	if _, _, ok := reg.Resolve("__"); ok {
		t.Fatal("name folding to empty must not resolve")
	}
}

func TestRegistryResolveRefusesAmbiguousFold(t *testing.T) {
	a := Build(Spec{Name: "web_fetch", Schema: map[string]any{"type": "object"}})
	reg := NewRegistry(NewWebFetch(WebFetchConfig{}), a)
	// Exact names still win.
	if _, name, ok := reg.Resolve("web_fetch"); !ok || name != "web_fetch" {
		t.Fatalf("exact match lost: %q %v", name, ok)
	}
	if _, _, ok := reg.Resolve("WEB_FETCH"); ok {
		t.Fatal("ambiguous fold must not pick one of two tools")
	}
}
