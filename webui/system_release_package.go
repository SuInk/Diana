// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"slices"
	"strings"

	"github.com/SuInk/diana/model/updater"
)

const (
	releaseManifestAsset    = "latest.json"
	releaseManifestMaxBytes = 1 << 20
)

// checksumsAssetNames 是规则没写 checksums 时依次找的校验文件。SHA256SUMS 是
// v0.9.0 及更早版本的名字，回退到这些版本、显示版本历史时还要认。
var checksumsAssetNames = []string{"SHA256SUMS.txt", "SHA256SUMS"}

func isChecksumsAsset(name string) bool {
	return slices.Contains(checksumsAssetNames, name)
}

// ReleasePackageRule 是 latest.json 里一个平台的更新规则：下哪个文件、拿哪个文件
// 校验、包是什么格式、程序在包里的位置、装好后叫什么。发布流程改包名或格式时只
// 改清单，已装好的更新器照着走，不用再在代码里留兼容。
type ReleasePackageRule struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Asset     string `json:"asset"`
	Format    string `json:"format"`
	Binary    string `json:"binary,omitempty"`
	InstallAs string `json:"install_as,omitempty"`
	Checksums string `json:"checksums,omitempty"`
}

func platformPackageRule(rules []ReleasePackageRule, goos, goarch string) (ReleasePackageRule, bool) {
	for _, rule := range rules {
		if rule.OS == goos && rule.Arch == goarch {
			return rule, true
		}
	}
	return ReleasePackageRule{}, false
}

// releasePackage 选出这台机器该下的包。版本信息自带规则（来自静态清单）就照规则
// 走；从 GitHub API 拿到的版本没有规则，就读该版本发布的 latest.json；两处都没有
// 本平台的规则才按命名约定找。下载地址只从版本的资产列表里取，不拿规则拼。
func (h *SystemUpdateHandler) releasePackage(ctx context.Context, entry ReleaseEntry) (updater.ReleasePackage, error) {
	rules := entry.Packages
	if len(rules) == 0 {
		rules = h.publishedPackageRules(ctx, entry)
	}
	rule, ok := platformPackageRule(rules, runtime.GOOS, runtime.GOARCH)
	if !ok {
		rule = ReleasePackageRule{Asset: h.releaseUpdater.ExpectedAssetName()}
	}
	archive, ok := entry.asset(rule.Asset)
	if !ok {
		return updater.ReleasePackage{}, fmt.Errorf("%w: %s", updater.ErrReleaseAssetMissing, rule.Asset)
	}
	checksumsNames := checksumsAssetNames
	if name := strings.TrimSpace(rule.Checksums); name != "" {
		checksumsNames = []string{name}
	}
	var checksums ReleaseAsset
	for _, name := range checksumsNames {
		if checksums, ok = entry.asset(name); ok {
			break
		}
	}
	if !ok {
		return updater.ReleasePackage{}, updater.ErrChecksumMissing
	}
	return updater.ReleasePackage{
		Tag:       entry.Tag,
		Archive:   updater.ReleaseAsset{Name: archive.Name, URL: archive.URL, Size: archive.Size},
		Checksums: updater.ReleaseAsset{Name: checksums.Name, URL: checksums.URL, Size: checksums.Size},
		Format:    rule.Format,
		Binary:    rule.Binary,
		InstallAs: rule.InstallAs,
	}, nil
}

// publishedPackageRules 读某个版本自带的 latest.json，按标签缓存。读不到就当没有
// 规则，退回命名约定；只缓存读成功的结果，网络抖一下不会把规则永久丢掉。
func (h *SystemUpdateHandler) publishedPackageRules(ctx context.Context, entry ReleaseEntry) []ReleasePackageRule {
	manifest, ok := entry.asset(releaseManifestAsset)
	if !ok {
		return nil
	}
	if cached, ok := h.packageRules.Load(entry.Tag); ok {
		return cached.([]ReleasePackageRule)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifest.URL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "diana-webui")
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var published struct {
		Releases []persistedReleaseEntry `json:"releases"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, releaseManifestMaxBytes)).Decode(&published); err != nil {
		return nil
	}
	var rules []ReleasePackageRule
	for _, release := range published.Releases {
		if release.Tag == entry.Tag {
			rules = append([]ReleasePackageRule(nil), release.Packages...)
			break
		}
	}
	h.packageRules.Store(entry.Tag, rules)
	return rules
}
