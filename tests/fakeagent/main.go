// fakeagent — a minimal v2 agent for local UI audits and performance
// runs: connects to the panel over WS, enrolls with a token, reports
// hello + runtime (N tunnels up) + usage every few seconds. NOT shipped.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var (
	server   = flag.String("server", "http://127.0.0.1:8000/ui", "panel base URL incl base path")
	token    = flag.String("token", "", "enroll token")
	name     = flag.String("name", "fake-agent", "agent name")
	role     = flag.String("role", "iran", "role: iran|foreign")
	tunnels  = flag.Int("tunnels", 3, "tunnels to report")
	traffic  = flag.Bool("traffic", true, "report wire/payload bytes")
	interval = flag.Duration("interval", 3*time.Second, "report interval")
)

type agentIdentity struct {
	Server   string `json:"server"`
	BasePath string `json:"base_path"`
	Key      string `json:"key"`
	Name     string `json:"name"`
}


func main() {
	flag.Parse()
	rand.New(rand.NewSource(time.Now().UnixNano()))
	base := strings.TrimRight(*server, "/")
	// enroll
	enrollURL := base + "/api/agent/enroll"
	resp, err := http.Post(enrollURL, "application/json",
		strings.NewReader(fmt.Sprintf(`{"name":%q,"hostname":%q,"os":"linux","arch":"amd64","caps":["matrix","v2"]}`,
			*name, *name)))
	if err != nil {
		log.Fatal("enroll: ", err)
	}
	var er struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	resp.Body.Close()
	if er.ID == "" {
		log.Fatal("enroll failed (token consumed? pass a fresh token via -token and the panel's bootstrap enroll route)")
	}
	fmt.Println("enrolled:", er.ID)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	stop := make(chan struct{})
	for {
		select {
		case <-stop:
			return
		default:
		}
		wsURL := "ws" + strings.TrimPrefix(base, "http") + "/api/agent/ws"
		hdr := http.Header{}
		hdr.Set("X-Agent-Id", er.ID)
		hdr.Set("X-Agent-Key", er.Key)
		ws, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
		if err != nil {
			select {
			case <-sig:
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		// hello
		hello := map[string]any{
			"v": 2, "typ": "hello", "version": "fake-1.0", "hostname": *name,
			"os": "linux", "arch": "amd64", "caps": []string{"matrix", "v2"},
			"engine_catalog": []map[string]any{{"id": "matrix", "version": "fake"}},
		}
		ws.WriteJSON(hello)
		go func() {
			<-sig
			ws.Close()
		}()
		tick := time.NewTicker(*interval)
		defer tick.Stop()
		var rx, tx uint64
		rt := make([]map[string]any, *tunnels)
		for i := range rt {
			rt[i] = map[string]any{"tunnel_id": fmt.Sprintf("t-%s-%d", *name, i),
				"engine": "matrix", "up": true, "latency_ms": 12.0 + float64(i),
				"wire_known": true, "payload_known": false}
		}
		for {
			select {
			case <-stop:
				ws.Close()
				return
			case <-tick.C:
				rx += uint64(100000 + rand.Intn(50000))
				tx += uint64(80000 + rand.Intn(40000))
				for i := range rt {
					rt[i]["wire_rx"], rt[i]["wire_tx"] = rx/uint64(*tunnels), tx/uint64(*tunnels)
				}
				ws.WriteJSON(map[string]any{"v": 2, "typ": "runtime", "tunnels": rt})
				ws.WriteJSON(map[string]any{"v": 2, "typ": "usage", "wire_rx": rx, "wire_tx": tx,
					"payload_known": false})
				_ = *traffic
			}
		}
	}
}
