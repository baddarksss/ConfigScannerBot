package countries

import (
	"testing"
)

func TestCodeByName(t *testing.T) {
	if got := CodeByName("Germany"); got != "DE" {
		t.Fatalf("Germany want DE, got %q", got)
	}
	if got := CodeByName("آلمان"); got != "DE" {
		t.Fatalf("آلمان want DE, got %q", got)
	}
	if got := CodeByName("France"); got != "FR" {
		t.Fatalf("France want FR, got %q", got)
	}
	if got := CodeByName("UnknownCountry"); got != "" {
		t.Fatalf("UnknownCountry want empty, got %q", got)
	}
}
