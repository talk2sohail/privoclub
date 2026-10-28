package main

import (
	"net/http"
	"testing"

	"privo.club/privo/internal/control"
)

// TestRoutesRegister catches invalid route patterns, which panic at startup.
func TestRoutesRegister(_ *testing.T) {
	(&control.API{}).Routes(http.NewServeMux())
}
