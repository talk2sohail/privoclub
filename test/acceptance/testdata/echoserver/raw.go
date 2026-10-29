//go:build acceptance

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// rawNotify is an MCP server written by hand, without the SDK, so that it can
// send what the SDK would never let a server send. It answers the handshake and
// an empty tools/list, then, right after notifications/initialized, sends one
// notification of the given method whose "params" is absent or null.
func rawNotify(method, params string) {
	var mu sync.Mutex
	out := bufio.NewWriter(os.Stdout)
	send := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintln(out, s)
		_ = out.Flush()
	}
	name, _ := json.Marshal(method)
	notification := fmt.Sprintf(`{"jsonrpc":"2.0","method":%s}`, name)
	if params == "null" {
		notification = fmt.Sprintf(`{"jsonrpc":"2.0","method":%s,"params":null}`, name)
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if len(m.ID) == 0 { // a notification from the gateway
			if m.Method == "notifications/initialized" {
				send(notification)
			}
			continue
		}
		switch m.Method {
		case "initialize":
			send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"raw","version":"0"}}}`, m.ID))
		case "tools/list":
			send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, m.ID))
		case "ping":
			send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{}}`, m.ID))
		default:
			send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}`, m.ID))
		}
	}
}
