package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const probeRoot = "/tmp/waza-native-probe"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	connection, networkErr := net.DialTimeout("tcp", "192.0.2.1:443", time.Second)
	if networkErr == nil {
		_ = connection.Close()
		return fmt.Errorf("network-none control unexpectedly reached TEST-NET address")
	}
	routes, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return err
	}
	if strings.Contains(string(routes), "00000000\t") {
		return fmt.Errorf("unexpected IPv4 route in network-none container")
	}
	emit(map[string]any{"event": "network_none_control", "routes": string(routes), "external_dial_error": networkErr.Error()})

	for _, path := range []string{"home/.copilot", "canary/tmp", "canary/.github/hooks"} {
		if err := os.MkdirAll(filepath.Join(probeRoot, path), 0700); err != nil {
			return err
		}
	}
	files := map[string]string{
		"canary/AGENTS.md":                         "OFFLINE_ANCESTOR_AGENT_SENTINEL\n",
		"canary/.github/copilot-instructions.md":   "OFFLINE_ANCESTOR_INSTRUCTION_SENTINEL\n",
		"home/.copilot/copilot-instructions.md":    "OFFLINE_HOME_INSTRUCTION_SENTINEL\n",
		"canary/.github/hooks/offline-canary.json": `{"version":1,"hooks":{"sessionStart":[{"type":"command","bash":"printf 'sessionStart\\n' >> /tmp/waza-native-probe/hook-sentinel","timeoutSec":2}],"userPromptSubmitted":[{"type":"command","bash":"printf 'userPromptSubmitted\\n' >> /tmp/waza-native-probe/hook-sentinel","timeoutSec":2}]}}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(probeRoot, name), []byte(content), 0600); err != nil {
			return err
		}
	}
	if output, err := exec.Command("/usr/bin/git", "-C", probeRoot+"/canary", "init", "--quiet").CombinedOutput(); err != nil {
		return fmt.Errorf("positive-control git init: %w: %s", err, output)
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	var mu sync.Mutex
	var mode string
	var child *exec.Cmd
	var requests []map[string]any
	var nativePIDs []int
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			panic(err)
		}
		mu.Lock()
		current := mode
		currentChild := child
		requests = append(requests, map[string]any{"mode": current, "path": r.URL.Path, "body": string(body)})
		mu.Unlock()

		procs := processes()
		mu.Lock()
		for pid, command := range procs {
			if strings.Contains(command, "/cache/copilot-sdk/") {
				nativePIDs = append(nativePIDs, pid)
			}
		}
		mu.Unlock()
		emit(map[string]any{"event": "provider_request", "mode": current, "processes": procs})

		if current == "hang_stopped" {
			var native []int
			for pid, command := range procs {
				if strings.Contains(command, "/cache/copilot-sdk/") {
					native = append(native, pid)
				}
			}
			if len(native) != 1 {
				panic("expected exactly one native process")
			}
			if err := syscall.Kill(native[0], syscall.SIGSTOP); err != nil {
				panic(err)
			}
			deadline := time.Now().Add(time.Second)
			for {
				state, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", native[0]))
				if err != nil {
					panic(err)
				}
				if strings.Contains(string(state), "State:\tT (stopped)") {
					emit(map[string]any{"event": "native_stopped", "pid": native[0], "state": "T (stopped)"})
					break
				}
				if time.Now().After(deadline) {
					panic("native SIGSTOP did not reach stopped-state barrier")
				}
				time.Sleep(time.Millisecond)
			}
		}
		if current == "sigterm" {
			if currentChild == nil || currentChild.Process == nil {
				panic("runner process was not available at provider admission")
			}
			if err := currentChild.Process.Signal(syscall.SIGTERM); err != nil {
				panic(err)
			}
		}
		if strings.HasPrefix(current, "hang") || current == "sigterm" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err = io.WriteString(w, `{"id":"chatcmpl_offline","object":"chat.completion","created":1700000000,"model":"offline-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8}}`)
		if err != nil {
			panic(err)
		}
	})}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()
	defer func() { _ = server.Close() }()

	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + probeRoot + "/home",
		"COPILOT_HOME=" + probeRoot + "/home/.copilot",
		"TMPDIR=" + probeRoot + "/canary/tmp",
		"PROBE_ROOT=" + probeRoot,
		"NO_COLOR=1",
		"WAZA_NO_UPDATE_CHECK=1",
		"COPILOT_BASE_URL=http://" + listener.Addr().String() + "/v1",
		"COPILOT_PROVIDER=openai",
		"COPILOT_WIRE_API=chat",
		"COPILOT_API_KEY=offline-synthetic-not-a-credential",
		"OTEL_SDK_DISABLED=true",
		"PROBE_LIBC=linux",
	}
	input, err := os.ReadFile("/input/copilot-unit.json")
	if err != nil {
		return err
	}
	var request map[string]any
	if err := json.Unmarshal(input, &request); err != nil {
		return err
	}
	inputMap, ok := request["input"].(map[string]any)
	if !ok {
		return fmt.Errorf("copilot unit fixture input must be an object")
	}
	eval, ok := inputMap["eval"].(string)
	if !ok {
		return fmt.Errorf("copilot unit fixture eval must be a string")
	}
	inputMap["eval"] = strings.ReplaceAll(eval, "gpt-4.1", "offline-model")

	reports := make([]map[string]any, 0, 6)
	var firstIdentity string
	for _, name := range []string{"reply", "hang", "hang_stopped", "sigterm", "sdk-positive", "reply-after-positive"} {
		request["timeout_ms"] = 15000
		if strings.HasPrefix(name, "hang") {
			request["timeout_ms"] = 2500
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		binary, args := "/waza", []string{"runner", "run"}
		if name == "sdk-positive" {
			binary, args = "/sdk-positive", nil
		}
		command := exec.CommandContext(ctx, binary, args...)
		command.Env, command.Dir = env, probeRoot+"/canary"
		payload, err := json.Marshal(request)
		if err != nil {
			cancel()
			return err
		}
		command.Stdin = bytes.NewReader(payload)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		mu.Lock()
		mode, child = name, command
		before := len(requests)
		nativePIDs = nil
		mu.Unlock()

		start := time.Now()
		err = command.Run()
		killed := ctx.Err() != nil
		cancel()
		code := 0
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				return err
			}
			code = exit.ExitCode()
		}
		mu.Lock()
		count := len(requests) - before
		ownedPIDs := append([]int(nil), nativePIDs...)
		mu.Unlock()
		hook, hookErr := os.ReadFile(probeRoot + "/hook-sentinel")
		if hookErr != nil && !os.IsNotExist(hookErr) {
			return hookErr
		}
		report := map[string]any{
			"name": name, "code": code, "elapsed_ms": time.Since(start).Milliseconds(),
			"externally_killed": killed, "provider_requests": count, "processes_after_exit": processes(),
			"hook_sentinel": string(hook), "stdout": stdout.String(), "stderr": stderr.String(),
		}
		reports = append(reports, report)
		emit(report)
		for _, pid := range ownedPIDs {
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !os.IsNotExist(err) {
				return fmt.Errorf("%s: owned native PID %d was not reaped: %v", name, pid, err)
			}
		}
		maxElapsed := 8 * time.Second
		if name == "reply" {
			maxElapsed = 20 * time.Second
		}
		if killed || count != 1 || len(processes()) != 0 || time.Since(start) >= maxElapsed {
			return fmt.Errorf("%s: execution/request/process/deadline contract failed", name)
		}
		if len(ownedPIDs) != 1 {
			return fmt.Errorf("%s: expected exactly one observed native process", name)
		}
		if name == "sdk-positive" {
			if code != 0 || string(hook) != "sessionStart\nuserPromptSubmitted\n" {
				return fmt.Errorf("positive canary control failed: code=%d hooks=%q", code, hook)
			}
			continue
		}

		expectedCode, expectedOutcome := 0, "success"
		if strings.HasPrefix(name, "hang") {
			expectedCode, expectedOutcome = 4, "timeout"
		} else if name == "sigterm" {
			expectedCode, expectedOutcome = 3, "cancelled"
		}
		var terminal map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &terminal); err != nil {
			return fmt.Errorf("%s: parse terminal response: %w", name, err)
		}
		if code != expectedCode || terminal["outcome"] != expectedOutcome || terminal["engine_calls"] != float64(1) {
			return fmt.Errorf("%s: wrong exit/outcome/engine calls", name)
		}
		selection, _ := json.Marshal(terminal["selection"])
		if string(selection) != `{"attempt":2,"shard":"shard-a","task":"selected","trial":2}` {
			return fmt.Errorf("%s: wrong exact selection: %s", name, selection)
		}
		identity, err := json.Marshal(terminal["identity"])
		if err != nil {
			return err
		}
		if name == "reply" {
			firstIdentity = string(identity)
		} else if name == "reply-after-positive" {
			if string(identity) != firstIdentity || string(hook) != "sessionStart\nuserPromptSubmitted\n" {
				return fmt.Errorf("repeat identity changed or restricted runner invoked hooks")
			}
		}
		if name != "reply-after-positive" && len(hook) != 0 {
			return fmt.Errorf("%s: restricted runner invoked ambient hook", name)
		}
	}

	if err := writeJSON("/evidence/reports.json", reports); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if err := writeJSON("/evidence/provider-requests.json", requests); err != nil {
		return err
	}
	for _, req := range requests {
		body := req["body"].(string)
		home := strings.Contains(body, "OFFLINE_HOME_INSTRUCTION_SENTINEL")
		ancestor := strings.Contains(body, "OFFLINE_ANCESTOR_INSTRUCTION_SENTINEL")
		agents := strings.Contains(body, "OFFLINE_ANCESTOR_AGENT_SENTINEL")
		if req["mode"] == "sdk-positive" {
			if !home || !ancestor || !agents {
				return fmt.Errorf("positive native instruction discovery did not activate all canaries")
			}
		} else if home || ancestor || agents {
			return fmt.Errorf("%s: ambient instructions leaked", req["mode"])
		}
		emit(map[string]any{"event": "canaries", "mode": req["mode"],
			"ancestor": ancestor, "agents": agents, "home": home})
	}
	emit(map[string]any{"event": "all_contract_assertions_passed", "cases": len(reports)})
	return nil
}

func processes() map[int]string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		panic(err)
	}
	found := map[int]string{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if os.IsNotExist(err) || err == syscall.ESRCH {
			continue
		}
		if err != nil {
			panic(err)
		}
		command := strings.ReplaceAll(string(raw), "\x00", " ")
		if strings.Contains(command, "/cache/copilot-sdk/") || strings.Contains(command, "/waza runner run") {
			found[pid] = command
		}
	}
	return found
}

func emit(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		panic(err)
	}
}

func writeJSON(path string, value any) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0644)
}
