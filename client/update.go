package main

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
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const updateAPI = "https://api.github.com/repos/neko233-com/nekossh/releases/latest"
const updateBase = "https://github.com/neko233-com/nekossh/releases/"

type updateAsset struct {
	Name, Digest string
	BrowserURL   string `json:"browser_download_url"`
	Size         int64
}
type updateRelease struct {
	TagName           string `json:"tag_name"`
	Draft, Prerelease bool
	Assets            []updateAsset
}
type updateManifest struct {
	Version      string
	SourceCommit string `json:"source_commit"`
	Artifacts    []struct {
		Name, SHA256 string
		Bytes        int64
	}
}
type updateCandidate struct {
	Version, Name, URL, SHA256 string
	Size                       int64
}

func updateVersion(raw string) ([3]uint64, error) {
	var result [3]uint64
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return result, errors.New("版本格式无效")
	}
	for i, part := range parts {
		if part == "" || len(part) > 10 || len(part) > 1 && part[0] == '0' {
			return result, errors.New("版本格式无效")
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return result, errors.New("版本格式无效")
			}
		}
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return result, errors.New("版本格式无效")
		}
		result[i] = n
	}
	return result, nil
}

func newerUpdate(candidate, current [3]uint64) bool {
	for i := range candidate {
		if candidate[i] != current[i] {
			return candidate[i] > current[i]
		}
	}
	return false
}

func updateHTTPClient() *http.Client {
	return &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedUpdateHost(req.URL) {
			return errors.New("更新重定向地址不可信")
		}
		return nil
	}}
}

func allowedUpdateHost(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com":
		return true
	}
	return false
}

func getUpdateBytes(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil || !allowedUpdateHost(u) {
		return nil, errors.New("更新地址不可信")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NekoSSH/"+version)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("无法连接项目发布服务")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, errors.New("项目发布服务暂时限制访问，请稍后重试")
		}
		return nil, fmt.Errorf("发布服务返回 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("发布信息无法读取或过大")
	}
	return data, nil
}

func findReleaseAsset(assets []updateAsset, name string) (updateAsset, error) {
	var found updateAsset
	count := 0
	for _, asset := range assets {
		if asset.Name == name {
			found = asset
			count++
		}
	}
	if count != 1 || found.Size <= 0 || !strings.HasPrefix(found.Digest, "sha256:") {
		return found, errors.New("发布资产或校验值无效")
	}
	if raw, err := hex.DecodeString(strings.TrimPrefix(found.Digest, "sha256:")); err != nil || len(raw) != sha256.Size {
		return found, errors.New("发布资产校验值无效")
	}
	return found, nil
}

func checkProjectUpdate(ctx context.Context, client *http.Client, current, goos, arch string) (*updateCandidate, error) {
	if goos != "windows" || arch != "amd64" && arch != "arm64" {
		return nil, errors.New("当前平台没有受支持的桌面更新")
	}
	currentVersion, err := updateVersion(current)
	if err != nil {
		return nil, err
	}
	data, err := getUpdateBytes(ctx, client, updateAPI, 1<<20)
	if err != nil {
		return nil, err
	}
	var release updateRelease
	if json.Unmarshal(data, &release) != nil || release.Draft || release.Prerelease || !strings.HasPrefix(release.TagName, "v") {
		return nil, errors.New("项目发布信息无效")
	}
	releaseVersion := strings.TrimPrefix(release.TagName, "v")
	parsed, err := updateVersion(releaseVersion)
	if err != nil {
		return nil, err
	}
	manifestAsset, err := findReleaseAsset(release.Assets, "manifest.json")
	if err != nil {
		return nil, err
	}
	expectedManifest := updateBase + "download/" + release.TagName + "/manifest.json"
	if manifestAsset.BrowserURL != expectedManifest || manifestAsset.Size > 256<<10 {
		return nil, errors.New("发布清单地址无效")
	}
	data, err = getUpdateBytes(ctx, client, expectedManifest, 256<<10)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != manifestAsset.Size || "sha256:"+hex.EncodeToString(sum[:]) != strings.ToLower(manifestAsset.Digest) {
		return nil, errors.New("发布清单校验失败")
	}
	var manifest updateManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Version != releaseVersion || len(manifest.SourceCommit) != 40 {
		return nil, errors.New("发布清单内容无效")
	}
	if _, err := hex.DecodeString(manifest.SourceCommit); err != nil {
		return nil, errors.New("发布来源无效")
	}
	name := "terminal233-setup-" + releaseVersion + "-" + arch + ".exe"
	asset, err := findReleaseAsset(release.Assets, name)
	if err != nil {
		return nil, errors.New("该平台的安装包或校验值不存在")
	}
	assetURL := updateBase + "download/" + release.TagName + "/" + name
	if asset.BrowserURL != assetURL || asset.Size > 1<<30 {
		return nil, errors.New("安装包地址或大小无效")
	}
	count := 0
	for _, item := range manifest.Artifacts {
		if item.Name == name {
			count++
			if item.Bytes != asset.Size || "sha256:"+strings.ToLower(item.SHA256) != strings.ToLower(asset.Digest) {
				return nil, errors.New("安装包校验值不一致")
			}
		}
	}
	if count != 1 {
		return nil, errors.New("发布清单中的安装包无效")
	}
	if !newerUpdate(parsed, currentVersion) {
		return nil, nil
	}
	return &updateCandidate{Version: releaseVersion, Name: name, URL: assetURL, SHA256: strings.ToLower(strings.TrimPrefix(asset.Digest, "sha256:")), Size: asset.Size}, nil
}

func downloadProjectUpdate(ctx context.Context, client *http.Client, candidate updateCandidate, target string) error {
	_, err := updateVersion(candidate.Version)
	if err != nil || candidate.Size <= 0 || candidate.Size > 1<<30 {
		return errors.New("安装包信息无效")
	}
	validName := candidate.Name == "terminal233-setup-"+candidate.Version+"-amd64.exe" || candidate.Name == "terminal233-setup-"+candidate.Version+"-arm64.exe"
	if !validName || candidate.URL != updateBase+"download/v"+candidate.Version+"/"+candidate.Name {
		return errors.New("安装包地址无效")
	}
	if bytes, err := hex.DecodeString(candidate.SHA256); err != nil || len(bytes) != sha256.Size {
		return errors.New("安装包校验值无效")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "NekoSSH/"+version)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("下载安装包失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载安装包失败（HTTP %d）", resp.StatusCode)
	}
	if _, err := os.Lstat(target); err == nil {
		return errors.New("目标文件已存在，不会覆盖")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".nekossh-update-*.part")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(temporary)
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, candidate.Size+1))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || n != candidate.Size || hex.EncodeToString(h.Sum(nil)) != candidate.SHA256 {
		return errors.New("安装包校验失败，未完成文件已清理")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := moveUpdateNoReplace(temporary, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("目标文件已存在，不会覆盖")
		}
		return err
	}
	return nil
}
