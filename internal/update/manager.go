package update

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Package struct{ Version, Digest, Notes, URL string }
type state struct {
	Repository      string
	Active, Pending *Package
}
type envelope struct {
	Payload json.RawMessage
	MAC     string
}
type Manager struct {
	mu           sync.Mutex
	dir, current string
	key          []byte
	client       *Client
	latest       *Release
	checked      time.Time
	checkErr     error
	restarting   bool
	working      bool
	jobError     string
}

func New(data, current string, key []byte) *Manager {
	return &Manager{dir: filepath.Join(data, "updates"), current: DisplayVersion(current), key: key, client: NewClient()}
}
func (m *Manager) mac(payload []byte) string {
	h := hmac.New(sha256.New, m.key)
	h.Write([]byte("cardword/update-state/v1\x00"))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
func (m *Manager) load() (state, error) {
	s := state{Repository: DefaultRepository}
	p := filepath.Join(m.dir, "state.json")
	b, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	var v envelope
	if e = json.Unmarshal(b, &v); e != nil || !hmac.Equal([]byte(v.MAC), []byte(m.mac(v.Payload))) {
		return s, errors.New("更新状态校验失败。")
	}
	e = json.Unmarshal(v.Payload, &s)
	return s, e
}
func (m *Manager) save(s state) error {
	if e := os.MkdirAll(m.dir, 0700); e != nil {
		return e
	}
	payload, e := json.Marshal(s)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(m.dir, ".state-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	e = json.NewEncoder(f).Encode(envelope{payload, m.mac(payload)})
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(m.dir, "state.json"))
}

type Status struct {
	Current    string   `json:"current"`
	Repository string   `json:"repository"`
	Latest     *Release `json:"latest,omitempty"`
	Available  bool     `json:"available"`
	Pending    *Release `json:"pending,omitempty"`
	Message    string   `json:"message,omitempty"`
	Working    bool     `json:"working"`
}

func (m *Manager) lookup(ctx context.Context, repo string, force bool) (*Release, error) {
	ttl := 5 * time.Minute
	if m.checkErr != nil {
		ttl = 30 * time.Second
	}
	if !force && time.Since(m.checked) < ttl {
		return m.latest, m.checkErr
	}
	r, e := m.client.Latest(ctx, repo)
	if ctx.Err() == nil {
		m.latest, m.checkErr, m.checked = r, e, time.Now()
	}
	return r, e
}
func (m *Manager) Check(ctx context.Context, force bool) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, e := m.load()
	v := Status{Current: m.current, Repository: s.Repository, Working: m.working, Message: m.jobError}
	if e != nil {
		return v, e
	}
	if p := s.Pending; p != nil {
		newer, err := Newer(p.Version, m.current)
		if err != nil {
			return v, err
		}
		if newer {
			v.Pending = &Release{Version: p.Version, Notes: p.Notes, URL: p.URL}
			return v, nil
		}
		// An externally upgraded launcher may already include the staged release.
		s.Pending = nil
		if err := m.save(s); err != nil {
			return v, err
		}
	}
	if m.working {
		v.Latest = m.latest
		v.Available = m.latest != nil
		return v, nil
	}
	r, e := m.lookup(ctx, s.Repository, force)
	if e != nil {
		v.Message = e.Error()
		return v, nil
	}
	v.Latest = r
	v.Available, e = Newer(r.Version, m.current)
	return v, e
}
func (m *Manager) Bind(raw string) error {
	repo, e := Repository(raw)
	if e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, e := m.load()
	if e != nil {
		return e
	}
	if s.Pending != nil || m.restarting || m.working {
		return errors.New("已有待重启更新，暂不能更换仓库。")
	}
	s.Repository = "https://github.com/" + repo
	if e = m.save(s); e == nil {
		m.checked = time.Time{}
		m.latest = nil
		m.checkErr = nil
		m.jobError = ""
	}
	return e
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (m *Manager) path(p *Package) (string, error) {
	if p == nil || !shaPattern.MatchString(p.Digest) {
		return "", errors.New("更新程序摘要无效。")
	}
	if _, e := versionParts(p.Version); e != nil {
		return "", e
	}
	name := "cardword-" + p.Digest
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(m.dir, name), nil
}
func (m *Manager) verified(p *Package) (string, error) {
	name, e := m.path(p)
	if e != nil {
		return "", e
	}
	info, e := os.Lstat(name)
	if e != nil {
		return "", e
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBinaryBytes {
		return "", errors.New("更新文件无效。")
	}
	f, e := os.Open(name)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	if hex.EncodeToString(h.Sum(nil)) != p.Digest {
		return "", errors.New("本地更新文件校验失败。")
	}
	return name, nil
}
func (m *Manager) Start(expected, repository string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, e := m.load()
	if e != nil {
		return e
	}
	if s.Pending != nil || m.restarting || m.working {
		return errors.New("已有更新正在处理或等待重启。")
	}
	if repository != s.Repository {
		return errors.New("更新仓库已变更，请重新检查。")
	}
	r := m.latest
	if r == nil || r.Version != expected || !r.Installable {
		return errors.New("请先检查可安装的最新版本。")
	}
	newer, e := Newer(r.Version, m.current)
	if e != nil {
		return e
	}
	if !newer {
		return errors.New("不能安装相同或更旧的版本。")
	}
	selected := *r
	m.working = true
	m.jobError = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		e := m.stageRelease(ctx, selected)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.working = false
		if e != nil {
			m.jobError = e.Error()
		}
	}()
	return nil
}
func (m *Manager) stageRelease(ctx context.Context, r Release) error {
	var e error
	if e = os.MkdirAll(m.dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(m.dir, ".download-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	e = m.client.Download(ctx, r, f)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	p := &Package{r.Version, r.Digest, r.Notes, r.URL}
	dest, e := m.path(p)
	if e != nil {
		return e
	}
	if e = os.Chmod(tmp, 0700); e != nil {
		return e
	}
	if _, e = m.verified(p); e != nil {
		if e = os.Rename(tmp, dest); e != nil {
			return e
		}
	}
	// Detect an incompatible executable or wrong build tag before interrupting the shop.
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, e := exec.CommandContext(probe, dest, "--version").Output()
	if e != nil || !strings.HasPrefix(string(out), "card word "+r.Version+" (") {
		return errors.New("新程序无法运行或版本不符。请确认数据卷允许执行程序。")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, e := m.load()
	if e != nil {
		return e
	}
	s.Pending = p
	return m.save(s)
}

// PrepareRestart authorizes one graceful handoff in this running worker.
func (m *Manager) PrepareRestart(expected string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, e := m.load()
	if e != nil {
		return e
	}
	if m.restarting || m.working || s.Pending == nil || s.Pending.Version != expected {
		return errors.New("没有对应的待重启版本。")
	}
	if _, e = m.verified(s.Pending); e != nil {
		return e
	}
	m.restarting = true
	return nil
}

// Executable selects a verified update; the immutable launcher remains the fallback.
func (m *Manager) Executable(launcher string, trial bool) (string, error) {
	s, e := m.load()
	if e != nil {
		return "", e
	}
	p := s.Active
	if trial {
		p = s.Pending
		if p == nil {
			return "", errors.New("没有待重启版本。")
		}
	}
	if p == nil {
		return launcher, nil
	}
	newer, e := Newer(p.Version, m.current)
	if e != nil {
		return "", e
	}
	if !newer {
		return launcher, nil
	}
	return m.verified(p)
}

// Confirm is called only after the new worker has opened its DB and acquired the listening port.
func (m *Manager) Confirm() error {
	s, e := m.load()
	if e != nil {
		return e
	}
	if s.Pending == nil || s.Pending.Version != m.current {
		return fmt.Errorf("待启用版本与运行版本 %s 不符", m.current)
	}
	s.Active, s.Pending = s.Pending, nil
	return m.save(s)
}
func (m *Manager) DiscardTrial(attempted string) (bool, error) {
	s, e := m.load()
	if e != nil || s.Pending == nil {
		return false, e
	}
	// The worker may have confirmed and staged a different update before exiting.
	if path, _ := m.path(s.Pending); attempted != "" && path != attempted {
		return false, nil
	}
	s.Pending = nil
	return true, m.save(s)
}

func (m *Manager) TrialPending() bool { s, e := m.load(); return e != nil || s.Pending != nil }
