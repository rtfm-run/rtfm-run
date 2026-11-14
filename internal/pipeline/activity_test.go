package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rtfm.run/internal/devbox"
)

func TestWatchActivity(t *testing.T) {
	old := activityPoll
	activityPoll = 10 * time.Millisecond
	t.Cleanup(func() { activityPoll = old })

	var mu sync.Mutex
	var got []map[string]any
	cfg := Config{OnEvent: func(kind string, data map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		data["kind"] = kind
		got = append(got, data)
	}}
	path := filepath.Join(t.TempDir(), "x.exec.jsonl")
	stop := cfg.watchActivity(context.Background(), "fix", path) // file doesn't exist yet

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	line := func(r devbox.ExecRecord) []byte { b, _ := json.Marshal(r); return append(b, '\n') }
	f.Write(line(devbox.ExecRecord{Kind: "exec", Command: "pip install pysc2", ExitCode: 0, Output: "SECRET OUTPUT"}))
	time.Sleep(50 * time.Millisecond)
	// A record split across two writes is relayed once, whole.
	p := line(devbox.ExecRecord{Kind: "progress", Command: "Pinning protobuf"})
	f.Write(p[:10])
	time.Sleep(30 * time.Millisecond)
	f.Write(p[10:])
	f.Write(line(devbox.ExecRecord{Kind: "edit_file", Command: "/work/project/README.md"}))
	f.Write([]byte("not json\n"))
	f.Close()
	stop() // drains what's left

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("events = %v", got)
	}
	if got[0]["kind"] != "command" || got[0]["command"] != "pip install pysc2" || got[0]["stage"] != "fix" || got[0]["output"] != nil {
		t.Errorf("command event = %v", got[0])
	}
	if got[1]["kind"] != "progress" || got[1]["message"] != "Pinning protobuf" {
		t.Errorf("progress event = %v", got[1])
	}
	if got[2]["file_op"] != "edit_file" || got[2]["path"] != "/work/project/README.md" {
		t.Errorf("file event = %v", got[2])
	}
}

func TestWatchActivityDisabled(t *testing.T) {
	Config{}.watchActivity(context.Background(), "newhire", "/nonexistent")() // no OnEvent: no-op
}
