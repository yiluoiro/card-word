// Package update checks GitHub releases and stages verified executable updates.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const DefaultRepository = "https://github.com/yiluoiro/card-word"
const MaxBinaryBytes int64 = 128 << 20

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Repository accepts only an explicit public GitHub repository, never arbitrary URLs.
func Repository(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", errors.New("请填写 https://github.com/所有者/仓库 格式的公开仓库地址。")
	}
	repo := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	repo = strings.TrimPrefix(repo, "/")
	if !repoPattern.MatchString(repo) {
		return "", errors.New("GitHub 仓库地址无效。")
	}
	return repo, nil
}

func versionParts(v string) ([3]uint64, error) {
	var out [3]uint64
	m := versionPattern.FindStringSubmatch(v)
	if m == nil {
		return out, errors.New("版本必须为 v主版本.次版本.修订版本，不支持预发布版本。")
	}
	for i := range out {
		n, err := strconv.ParseUint(m[i+1], 10, 32)
		if err != nil {
			return out, errors.New("版本号超出范围。")
		}
		out[i] = n
	}
	return out, nil
}

func Newer(next, current string) (bool, error) {
	a, err := versionParts(next)
	if err != nil {
		return false, err
	}
	b, err := versionParts(current)
	if err != nil {
		return false, err
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i], nil
		}
	}
	return false, nil
}

func DisplayVersion(v string) string { return "v" + strings.TrimPrefix(v, "v") }

func AssetName(tag, goos, arch string) string {
	n := "cardword_" + DisplayVersion(tag) + "_" + goos + "_" + arch
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

type Release struct {
	Version     string `json:"version"`
	Notes       string `json:"notes"`
	URL         string `json:"url"`
	PublishedAt string `json:"published_at"`
	Installable bool   `json:"installable"`
	Reason      string `json:"reason,omitempty"`
	AssetURL    string `json:"-"`
	Digest      string `json:"-"`
	Size        int64  `json:"-"`
}

type githubRelease struct {
	Tag         string `json:"tag_name"`
	Body        string `json:"body"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"assets"`
}

type Client struct{ HTTP *http.Client }

func trustedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return false
	}
	switch u.Host {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}

func NewClient() *Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 15 * time.Second
	return &Client{HTTP: &http.Client{Transport: t, Timeout: 5 * time.Minute}}
}

func (c *Client) get(ctx context.Context, address string) (*http.Response, error) {
	if !trustedURL(address) {
		return nil, errors.New("更新地址不在可信 GitHub 下载域名内。")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "CardWord-Updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := *c.HTTP
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !trustedURL(r.URL.String()) {
			return errors.New("更新下载重定向被拒绝。")
		}
		return nil
	}
	return client.Do(req)
}

func (c *Client) Latest(ctx context.Context, repository string) (*Release, error) {
	repo, err := Repository(repository)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := c.get(ctx, "https://api.github.com/repos/"+repo+"/releases/latest")
	if err != nil {
		return nil, fmt.Errorf("连接 GitHub 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, errors.New("仓库尚未发布正式版本，或仓库不存在／不可公开访问。")
	}
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		return nil, errors.New("GitHub 请求受限，请稍后再检查。")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub 返回 HTTP %d。", resp.StatusCode)
	}
	var raw githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&raw); err != nil {
		return nil, errors.New("无法解析 GitHub 版本信息。")
	}
	if raw.Draft || raw.Prerelease {
		return nil, errors.New("更新源未提供正式版本。")
	}
	if _, err := versionParts(raw.Tag); err != nil {
		return nil, err
	}
	r := &Release{Version: DisplayVersion(raw.Tag), Notes: raw.Body, PublishedAt: raw.PublishedAt, URL: "https://github.com/" + repo + "/releases/tag/" + url.PathEscape(raw.Tag)}
	name := AssetName(raw.Tag, runtime.GOOS, runtime.GOARCH)
	wantURL := "https://github.com/" + repo + "/releases/download/" + url.PathEscape(raw.Tag) + "/" + name
	r.Reason = "此版本未提供当前系统的更新程序：" + name
	for _, a := range raw.Assets {
		if a.Name != name {
			continue
		}
		if a.URL != wantURL || a.Size < 1 || a.Size > MaxBinaryBytes {
			r.Reason = "更新程序的地址或体积无效。"
			break
		}
		digest := strings.TrimPrefix(a.Digest, "sha256:")
		b, err := hex.DecodeString(digest)
		if !strings.HasPrefix(a.Digest, "sha256:") || err != nil || len(b) != sha256.Size {
			r.Reason = "更新程序缺少 GitHub SHA-256 校验信息。"
			break
		}
		r.AssetURL, r.Digest, r.Size, r.Installable, r.Reason = a.URL, strings.ToLower(digest), a.Size, true, ""
		break
	}
	return r, nil
}

func (c *Client) Download(ctx context.Context, r Release, dst io.Writer) error {
	if !r.Installable || r.Size <= 0 || r.Size > MaxBinaryBytes {
		return errors.New("没有可用的更新程序。")
	}
	resp, err := c.get(ctx, r.AssetURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载更新失败：HTTP %d", resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(resp.Body, r.Size+1))
	if err != nil {
		return err
	}
	if n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.Digest {
		return errors.New("更新程序的大小或 SHA-256 不匹配，已拒绝安装。")
	}
	return nil
}
