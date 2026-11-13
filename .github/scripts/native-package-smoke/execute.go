package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const commandTimeout = 2 * time.Minute

func runBinary(ctx context.Context, dir, binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s %s: %w\n%s", filepath.Base(binary), strings.Join(args, " "), err, out.String())
	}
	return out.String(), nil
}

func checkVersion(ctx context.Context, dir, binary, version string) error {
	out, err := runBinary(ctx, dir, binary, "--version")
	if err != nil {
		return err
	}
	if out != "waza version "+version+"\n" {
		return fmt.Errorf("--version printed %q, want waza version %s", out, version)
	}
	return nil
}

func checkHelp(ctx context.Context, dir, binary string) error {
	out, err := runBinary(ctx, dir, binary, "--help")
	if err != nil {
		return err
	}
	for _, want := range []string{"Usage:\n  waza [command]", "\n  run ", "\n  serve "} {
		if !strings.Contains(out, want) {
			return fmt.Errorf("--help output lacks %q", want)
		}
	}
	return nil
}

// checkMock runs the shared offline integration contract, which requires a
// complete error-free mock outcome and a rejecting Copilot runtime guard.
func checkMock(ctx context.Context, repo, script, binary string) error {
	if os.Getenv("COPILOT_CLI_PATH") == "" {
		return fmt.Errorf("mock smoke requires the rejecting COPILOT_CLI_PATH guard")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script, filepath.ToSlash(binary))
	cmd.Dir = repo
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("offline mock integration: %w\n%s", err, out.String())
	}
	if _, err := os.Stat(os.Getenv("COPILOT_CLI_PATH") + ".invoked"); err == nil {
		return fmt.Errorf("mock smoke launched the Copilot runtime")
	}
	return nil
}

var assetReference = regexp.MustCompile(`(?:src|href)="(/assets/[^"?#]+)"`)

// checkDashboard serves from an empty directory so only the embedded
// dashboard can answer, then fetches the index and every referenced asset.
func checkDashboard(ctx context.Context, binary string) (int, error) {
	work, err := os.MkdirTemp("", "waza-native-dashboard-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(work)
	results := filepath.Join(work, "results")
	if err := os.Mkdir(results, 0o700); err != nil {
		return 0, err
	}
	port, err := freePort()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "serve", "--no-browser", "--port", strconv.Itoa(port), "--results-dir", results)
	cmd.Dir = work
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	exited := make(chan error, 1)
	// Closing after the send lets cleanup finish even when waitHealthy took the result.
	go func() { exited <- cmd.Wait(); close(exited) }()
	defer func() {
		_ = cmd.Process.Kill()
		<-exited
	}()
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Timeout: 10 * time.Second}
	if err := waitHealthy(ctx, client, base, exited); err != nil {
		return 0, fmt.Errorf("%w\n%s", err, log.String())
	}
	index, err := fetch(ctx, client, base+"/", "text/html")
	if err != nil {
		return 0, err
	}
	if !bytes.Contains(index, []byte(`<div id="root"></div>`)) {
		return 0, fmt.Errorf("dashboard index is not the production dashboard document")
	}
	assets := assetReference.FindAllSubmatch(index, -1)
	scripts := 0
	for _, match := range assets {
		asset := string(match[1])
		want := ""
		switch path.Ext(asset) {
		case ".js":
			want, scripts = "javascript", scripts+1
		case ".css":
			want = "text/css"
		}
		if _, err := fetch(ctx, client, base+asset, want); err != nil {
			return 0, err
		}
	}
	if scripts == 0 {
		return 0, fmt.Errorf("dashboard index references no script assets")
	}
	return len(assets), nil
}

func waitHealthy(ctx context.Context, client *http.Client, base string, exited <-chan error) error {
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("dashboard server exited before becoming ready: %v", err)
		case <-ctx.Done():
			return fmt.Errorf("dashboard server did not become ready: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/health", nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
}

func fetch(ctx context.Context, client *http.Client, url, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || len(body) == 0 {
		return nil, fmt.Errorf("GET %s: status %d with %d bytes", url, resp.StatusCode, len(body))
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if contentType != "" && (err != nil || !strings.Contains(mediaType, contentType)) {
		return nil, fmt.Errorf("GET %s: content type %q, want %s", url, resp.Header.Get("Content-Type"), contentType)
	}
	return body, nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
