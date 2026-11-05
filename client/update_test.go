package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type updateRoundTrip func(*http.Request) (*http.Response, error)

func (f updateRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func updateResponse(body []byte) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}
func digestBytes(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

type heldUpdateReader struct {
	started, resume chan struct{}
	data            []byte
	done            bool
}

func (r *heldUpdateReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	close(r.started)
	<-r.resume
	return copy(p, r.data), nil
}
func (*heldUpdateReader) Close() error { return nil }

func updateFixture(t *testing.T) (updateRelease, []byte, []byte) {
	t.Helper()
	installer := []byte("fake unsigned installer")
	manifest := updateManifest{Version: "1.2.3", SourceCommit: strings.Repeat("a", 40)}
	for _, arch := range []string{"amd64", "arm64"} {
		manifest.Artifacts = append(manifest.Artifacts, struct {
			Name, SHA256 string
			Bytes        int64
		}{Name: "terminal233-setup-1.2.3-" + arch + ".exe", SHA256: digestBytes(installer), Bytes: int64(len(installer))})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release := updateRelease{TagName: "v1.2.3"}
	release.Assets = append(release.Assets, updateAsset{Name: "manifest.json", Digest: "sha256:" + digestBytes(data), Size: int64(len(data)), BrowserURL: updateBase + "download/v1.2.3/manifest.json"})
	for _, item := range manifest.Artifacts {
		release.Assets = append(release.Assets, updateAsset{Name: item.Name, Digest: "sha256:" + item.SHA256, Size: item.Bytes, BrowserURL: updateBase + "download/v1.2.3/" + item.Name})
	}
	return release, data, installer
}

func fixtureClient(t *testing.T, release updateRelease, manifest []byte) *http.Client {
	t.Helper()
	releaseBytes, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: updateRoundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case updateAPI:
			return updateResponse(releaseBytes), nil
		case updateBase + "download/v1.2.3/manifest.json":
			return updateResponse(manifest), nil
		}
		t.Errorf("unexpected request: %s", r.URL.Hostname())
		return nil, io.EOF
	})}
}

func TestProjectUpdateRequiresMatchingReleaseManifestAndArchitecture(t *testing.T) {
	release, manifest, _ := updateFixture(t)
	for _, arch := range []string{"amd64", "arm64"} {
		got, err := checkProjectUpdate(context.Background(), fixtureClient(t, release, manifest), "1.2.2", "windows", arch)
		if err != nil || got == nil || got.Name != "terminal233-setup-1.2.3-"+arch+".exe" {
			t.Fatalf("wrong platform result: %v %v", got, err)
		}
	}
	if got, err := checkProjectUpdate(context.Background(), fixtureClient(t, release, manifest), "1.2.3", "windows", "amd64"); err != nil || got != nil {
		t.Fatal("same version was offered")
	}
	if _, err := checkProjectUpdate(context.Background(), fixtureClient(t, release, manifest), "1.2.2", "windows", "386"); err == nil {
		t.Fatal("32-bit update was accepted")
	}
	if _, err := checkProjectUpdate(context.Background(), fixtureClient(t, release, append(manifest, ' ')), "1.2.2", "windows", "amd64"); err == nil {
		t.Fatal("modified manifest was accepted")
	}
	release.Assets[1].BrowserURL = "https://attacker.example/setup.exe"
	if _, err := checkProjectUpdate(context.Background(), fixtureClient(t, release, manifest), "1.2.2", "windows", "amd64"); err == nil {
		t.Fatal("foreign download URL was accepted")
	}
}

func TestUpdateVersionAndRedirectRules(t *testing.T) {
	for _, raw := range []string{"v1.2.3", "1.2", "1.2.3-rc1", "1.02.3", "1.2.+3", "4294967296.1.1"} {
		if _, err := updateVersion(raw); err == nil {
			t.Fatalf("accepted invalid version %q", raw)
		}
	}
	a, _ := updateVersion("1.10.0")
	b, _ := updateVersion("1.9.99")
	if !newerUpdate(a, b) {
		t.Fatal("numeric version comparison failed")
	}
	for _, raw := range []string{"http://github.com/x", "https://github.com.attacker.example/x", "https://u@github.com/x", "https://github.com:444/x"} {
		u, _ := url.Parse(raw)
		if allowedUpdateHost(u) {
			t.Fatalf("accepted unsafe URL %q", raw)
		}
	}
	u, _ := url.Parse("https://release-assets.githubusercontent.com/x")
	if !allowedUpdateHost(u) {
		t.Fatal("GitHub asset host rejected")
	}
}

func TestDownloadedUpdateMustMatchHashAndNeverOverwrite(t *testing.T) {
	_, _, installer := updateFixture(t)
	candidate := updateCandidate{Version: "1.2.3", Name: "terminal233-setup-1.2.3-amd64.exe", URL: updateBase + "download/v1.2.3/terminal233-setup-1.2.3-amd64.exe", SHA256: digestBytes(installer), Size: int64(len(installer))}
	client := &http.Client{Transport: updateRoundTrip(func(*http.Request) (*http.Response, error) { return updateResponse(installer), nil })}
	root := t.TempDir()
	target := filepath.Join(root, "setup.exe")
	if err := downloadProjectUpdate(context.Background(), client, candidate, target); err != nil {
		t.Fatal(err)
	}
	if err := downloadProjectUpdate(context.Background(), client, candidate, target); err == nil {
		t.Fatal("existing installer was overwritten")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != string(installer) {
		t.Fatal("verified installer changed")
	}
	bad := candidate
	bad.SHA256 = strings.Repeat("0", 64)
	badPath := filepath.Join(root, "bad.exe")
	if err := downloadProjectUpdate(context.Background(), client, bad, badPath); err == nil {
		t.Fatal("corrupt download was accepted")
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Fatal("failed download was not cleaned up")
	}
}

func TestUpdateDownloadDoesNotExposeUnverifiedFileOrOverwriteRace(t *testing.T) {
	_, _, installer := updateFixture(t)
	candidate := updateCandidate{Version: "1.2.3", Name: "terminal233-setup-1.2.3-amd64.exe", URL: updateBase + "download/v1.2.3/terminal233-setup-1.2.3-amd64.exe", SHA256: digestBytes(installer), Size: int64(len(installer))}
	held := &heldUpdateReader{started: make(chan struct{}), resume: make(chan struct{}), data: installer}
	client := &http.Client{Transport: updateRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: held}, nil
	})}
	dir := t.TempDir()
	target := filepath.Join(dir, "setup.exe")
	done := make(chan error, 1)
	go func() { done <- downloadProjectUpdate(context.Background(), client, candidate, target) }()
	<-held.started
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("unverified installer appeared at its final path")
	}
	if err := os.WriteFile(target, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	close(held.resume)
	if err := <-done; err == nil {
		t.Fatal("destination created during download was overwritten")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "existing" {
		t.Fatal("existing target changed")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, ".nekossh-update-*.part")); len(files) != 0 {
		t.Fatal("temporary installer was not cleaned up")
	}
}

func TestUpdateRedirectNeverLeavesTrustedHosts(t *testing.T) {
	calls := 0
	client := updateHTTPClient()
	client.Transport = updateRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://attacker.example/file"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := getUpdateBytes(context.Background(), client, updateAPI, 1024); err == nil || calls != 1 {
		t.Fatal("untrusted redirect was followed")
	}
}

func TestLiveProjectReleaseWhenExplicitlyRequested(t *testing.T) {
	if os.Getenv("NEKOSSH_TEST_LIVE_UPDATES") != "1" {
		t.Skip("live release check is opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := updateHTTPClient()
	token, forceLimit := os.Getenv("NEKOSSH_TEST_GH_TOKEN"), os.Getenv("NEKOSSH_TEST_FORCE_RATE_LIMIT") == "1"
	if token != "" || forceLimit {
		client.Transport = updateRoundTrip(func(r *http.Request) (*http.Response, error) {
			if forceLimit && r.URL.String() == updateAPI {
				return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("rate limited"))}, nil
			}
			copy := r.Clone(r.Context())
			if token != "" && copy.URL.Hostname() == "api.github.com" {
				copy.Header.Set("Authorization", "Bearer "+token)
			}
			return http.DefaultTransport.RoundTrip(copy)
		})
	}
	candidate, err := checkProjectUpdate(ctx, client, "0.3.0", "windows", "amd64")
	if err != nil || candidate == nil {
		t.Fatalf("published update could not be verified: %v", err)
	}
	if err := downloadProjectUpdate(ctx, client, *candidate, filepath.Join(t.TempDir(), candidate.Name)); err != nil {
		t.Fatalf("published installer could not be verified: %v", err)
	}
}

func fallbackFixtureClient(manifest, sums []byte, location string) *http.Client {
	return &http.Client{Transport: updateRoundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case updateAPI:
			return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("rate limited"))}, nil
		case updateBase + "latest":
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		case updateBase + "download/v1.2.3/SHA256SUMS.txt":
			return updateResponse(sums), nil
		case updateBase + "download/v1.2.3/manifest.json":
			return updateResponse(manifest), nil
		default:
			return nil, io.EOF
		}
	})}
}

func TestRateLimitedUpdateUsesVerifiedOfficialReleaseAssets(t *testing.T) {
	_, manifest, installer := updateFixture(t)
	sums := []byte(digestBytes(manifest) + "  manifest.json\r\n" + digestBytes(installer) + "  terminal233-setup-1.2.3-amd64.exe\r\n")
	client := fallbackFixtureClient(manifest, sums, updateBase+"tag/v1.2.3")
	got, err := checkProjectUpdate(context.Background(), client, "1.2.2", "windows", "amd64")
	if err != nil || got == nil || got.SHA256 != digestBytes(installer) || got.Size != int64(len(installer)) {
		t.Fatalf("fallback failed: %+v %v", got, err)
	}
	if got, err = checkProjectUpdate(context.Background(), client, "1.2.3", "windows", "amd64"); err != nil || got != nil {
		t.Fatal("fallback offered current version")
	}
	if _, err = checkProjectUpdate(context.Background(), client, "1.2.2", "windows", "arm64"); err == nil {
		t.Fatal("fallback accepted absent architecture checksum")
	}
	if _, err = checkProjectUpdate(context.Background(), client, "1.2.2", "windows", "386"); err == nil {
		t.Fatal("fallback accepted unsupported architecture")
	}
	baseTransport := client.Transport
	client.Transport = updateRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == updateAPI {
			return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return baseTransport.RoundTrip(r)
	})
	if got, err = checkProjectUpdate(context.Background(), client, "1.2.2", "windows", "amd64"); err != nil || got == nil {
		t.Fatal("HTTP 429 did not use verified fallback")
	}
}

func TestRateLimitedUpdateRejectsTamperingAndUntrustedTags(t *testing.T) {
	_, manifest, installer := updateFixture(t)
	good := digestBytes(manifest) + "  manifest.json\n" + digestBytes(installer) + "  terminal233-setup-1.2.3-amd64.exe\n"
	for _, location := range []string{"https://attacker.example/v1.2.3", updateBase + "tag/v1.2.3-rc1", updateBase + "tag/v1.2.3?bad=1", updateBase + "download/v1.2.3/manifest.json"} {
		if _, err := checkProjectUpdate(context.Background(), fallbackFixtureClient(manifest, []byte(good), location), "1.2.2", "windows", "amd64"); err == nil {
			t.Fatalf("accepted untrusted release %q", location)
		}
	}
	for _, sums := range []string{strings.Repeat("0", 64) + "  manifest.json\n" + digestBytes(installer) + "  terminal233-setup-1.2.3-amd64.exe\n", good + digestBytes(manifest) + "  manifest.json\n", digestBytes(manifest) + "  manifest.json\n" + strings.Repeat("0", 64) + "  terminal233-setup-1.2.3-amd64.exe\n", good + "bad line\n"} {
		if _, err := checkProjectUpdate(context.Background(), fallbackFixtureClient(manifest, []byte(sums), updateBase+"tag/v1.2.3"), "1.2.2", "windows", "amd64"); err == nil {
			t.Fatal("accepted corrupted checksum list")
		}
	}
	if _, err := checkProjectUpdate(context.Background(), fallbackFixtureClient(append(manifest, ' '), []byte(good), updateBase+"tag/v1.2.3"), "1.2.2", "windows", "amd64"); err == nil {
		t.Fatal("accepted modified fallback manifest")
	}
}

func TestUpdateDoesNotFallbackAfterInvalidAPIRelease(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: updateRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return updateResponse([]byte(`{"tag_name":"v1.2.3","draft":true}`)), nil
	})}
	if _, err := checkProjectUpdate(context.Background(), client, "1.0.0", "windows", "amd64"); err == nil || calls != 1 {
		t.Fatal("invalid API metadata triggered weaker fallback")
	}
}
