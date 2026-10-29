package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"privo.club/privo/internal/buildinfo"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb, func(string) string { return "" }); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var v map[string]string
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v["version"] != buildinfo.Version || v["commit"] != "" {
		t.Fatalf("output %q, want version %q and no commit", out.String(), buildinfo.Version)
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"nope"}, &out, &errb, func(string) string { return "" }); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "unknown command") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestServeRejectsBadConfig(t *testing.T) {
	var out, errb bytes.Buffer
	getenv := func(k string) string {
		if k == "PRIVO_LISTEN" {
			return "bad"
		}
		return ""
	}
	if code := run([]string{"serve"}, &out, &errb, getenv); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "PRIVO_LISTEN") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestHealthcheckFailsWhenNothingListens(t *testing.T) {
	var out, errb bytes.Buffer
	getenv := func(k string) string {
		if k == "PRIVO_LISTEN" {
			return "127.0.0.1:1"
		}
		return ""
	}
	if code := run([]string{"healthcheck"}, &out, &errb, getenv); code != 1 {
		t.Fatalf("exit %d", code)
	}
}
