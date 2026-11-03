package buildinfo

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	got := String("v1.2.3")
	if !strings.HasPrefix(got, "v1.2.3") {
		t.Errorf("String = %q", got)
	}
	// With VCS info the commit follows in parentheses; without it the version stands alone.
	if got != "v1.2.3" && !strings.HasPrefix(got, "v1.2.3 (commit ") {
		t.Errorf("String = %q", got)
	}
}

func TestCredit(t *testing.T) {
	got := Credit("tool", "v9", "does things")
	for _, want := range []string{"tool v9 - does things", "MIT License", "github.com/msstnk/go-pstgen"} {
		if !strings.Contains(got, want) {
			t.Errorf("Credit lacks %q: %q", want, got)
		}
	}
}
