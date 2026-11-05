package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReleaseBoundaries(t *testing.T) {
	for _, url := range []string{"http://github.com/a/b", "https://evil.com/a/b", "https://github.com@evil.com/a/b", "https://github.com/a/../b", "https://github.com/a/b?host=x", "https://github.com:8443/a/b"} {
		if _, e := Repository(url); e == nil {
			t.Fatal(url)
		}
	}
	if v, e := Repository(DefaultRepository + ".git"); e != nil || v != "yiluoiro/card-word" {
		t.Fatal(v, e)
	}
	for _, v := range []string{"v1.0.0-rc.1", "v01.0.1", "main", "v2.3", "v2.3.4/../bad"} {
		if _, e := Newer(v, "v1.0.0"); e == nil {
			t.Fatal(v)
		}
	}
	if ok, e := Newer("v1.10.0", "v1.9.9"); !ok || e != nil {
		t.Fatal(ok, e)
	}
	if ok, _ := Newer("v1.0.0", "v1.0.1"); ok {
		t.Fatal("accepted downgrade")
	}
}

func TestObsoletePendingAndTrialOwnership(t *testing.T) {
	m := New(t.TempDir(), "v1.2.0", []byte("test-key"))
	p := &Package{Version: "v1.1.0", Digest: strings.Repeat("a", 64)}
	if e := m.save(state{Repository: DefaultRepository, Pending: p}); e != nil {
		t.Fatal(e)
	}
	m.latest, m.checked = &Release{Version: "v1.2.0"}, time.Now()
	status, e := m.Check(context.Background(), false)
	if e != nil || status.Pending != nil || status.Available {
		t.Fatal(status, e)
	}
	if e = m.Bind(DefaultRepository); e != nil {
		t.Fatal("obsolete pending blocked repository binding:", e)
	}
	p.Version = "v1.3.0"
	if e = m.save(state{Repository: DefaultRepository, Pending: p}); e != nil {
		t.Fatal(e)
	}
	if discarded, e := m.DiscardTrial("another-executable"); e != nil || discarded {
		t.Fatal("discarded a different update", discarded, e)
	}
	path, _ := m.path(p)
	if discarded, e := m.DiscardTrial(path); e != nil || !discarded {
		t.Fatal("failed to discard trial", discarded, e)
	}
}

func TestDownloadStageAndActivation(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	source := filepath.Join(dir, "fixture.go")
	if e := os.WriteFile(source, []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"card word v1.2.0 (test, built test)\")}"), 0600); e != nil {
		t.Fatal(e)
	}
	if out, e := exec.Command("go", "build", "-o", bin, source).CombinedOutput(); e != nil {
		t.Fatalf("%s: %v", out, e)
	}
	pkg, e := os.ReadFile(bin)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(pkg)
	digest := hex.EncodeToString(sum[:])
	name := AssetName("v1.2.0", runtime.GOOS, runtime.GOARCH)
	download := DefaultRepository + "/releases/download/v1.2.0/" + name
	release, _ := json.Marshal(map[string]any{"tag_name": "v1.2.0", "body": "Fix order search", "assets": []any{map[string]any{"name": name, "size": len(pkg), "digest": "sha256:" + digest, "browser_download_url": download}}})
	checks := 0
	m := New(dir, "v1.0.0", []byte("test-key"))
	m.client.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		var body []byte
		switch r.URL.String() {
		case "https://api.github.com/repos/yiluoiro/card-word/releases/latest":
			checks++
			body = release
		case download:
			body = pkg
		default:
			return nil, fmt.Errorf("unexpected URL %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})
	ctx := context.Background()
	for _, force := range []bool{false, false, true} {
		v, e := m.Check(ctx, force)
		if e != nil || !v.Available || v.Latest.Notes != "Fix order search" {
			t.Fatal(v, e)
		}
	}
	if checks != 2 {
		t.Fatal("cache/force", checks)
	}
	if e = m.Start("v1.1.9", DefaultRepository); e == nil {
		t.Fatal("stale selection accepted")
	}
	if e = m.Start("v1.2.0", DefaultRepository); e != nil {
		t.Fatal(e)
	}
	for limit := time.Now().Add(20 * time.Second); ; {
		v, e := m.Check(ctx, false)
		if e != nil {
			t.Fatal(e)
		}
		if !v.Working {
			if v.Pending == nil {
				t.Fatal(v)
			}
			break
		}
		if time.Now().After(limit) {
			t.Fatal("timed out staging")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e = m.Bind("https://github.com/other/source"); e == nil {
		t.Fatal("changed repository mid-update")
	}
	if p, e := m.Executable("original", false); p != "original" || e != nil {
		t.Fatal("early activation", p, e)
	}
	if e = m.PrepareRestart("v1.2.0"); e != nil {
		t.Fatal(e)
	}
	path, e := m.Executable("original", true)
	if e != nil {
		t.Fatal(e)
	}
	next := New(dir, "v1.2.0", m.key)
	if e = next.Confirm(); e != nil {
		t.Fatal(e)
	}
	if p, e := m.Executable("original", false); p != path || e != nil {
		t.Fatal("activation lost", p, e)
	}
	if e = os.WriteFile(path, []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Executable("original", false); e == nil {
		t.Fatal("local tampering accepted")
	}
	wrong := New(dir, "v1.0.0", []byte("different"))
	if _, e = wrong.load(); e == nil {
		t.Fatal("untrusted state accepted")
	}
	bad := Release{AssetURL: download, Size: int64(len(pkg)), Digest: strings.Repeat("0", 64), Installable: true}
	if e = m.client.Download(ctx, bad, io.Discard); e == nil {
		t.Fatal("corrupt download accepted")
	}
}
