package gateway

import (
	"strings"
	"testing"
)

func TestDisabledByUserMessage(t *testing.T) {
	withURL := &Gateway{BaseURL: "https://app.privo.club/"}
	msg := withURL.disabledByUserMsg("github", "delete_repo")
	for _, want := range []string{`"delete_repo"`, `"github"`, "https://app.privo.club/admin/start", "turned off"} {
		if !strings.Contains(msg, want) {
			t.Errorf("with BaseURL: %q lacks %q", msg, want)
		}
	}
	noURL := &Gateway{}
	msg2 := noURL.disabledByUserMsg("github", "delete_repo")
	if !strings.Contains(msg2, "Start page") || strings.Contains(msg2, "http") {
		t.Errorf("without BaseURL: %q", msg2)
	}
}
