package webui

import "testing"

// 旧包名 diana-webui-… 不再兼容：只有当前包名的资产才算可用。
func TestReleaseAssetMatchesCurrentNameOnly(t *testing.T) {
	current := ReleaseAsset{Name: "diana-linux-amd64.tar.gz", URL: "https://example.test/new"}
	legacy := ReleaseAsset{Name: "diana-webui-linux-amd64.tar.gz", URL: "https://example.test/old"}
	for _, tc := range []struct {
		name   string
		assets []ReleaseAsset
		want   string
	}{
		{"both", []ReleaseAsset{legacy, current}, current.Name},
		{"new only", []ReleaseAsset{current}, current.Name},
		{"old only", []ReleaseAsset{legacy}, ""},
		{"missing", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := (ReleaseEntry{Assets: tc.assets}).asset(current.Name)
			if ok != (tc.want != "") || got.Name != tc.want {
				t.Fatalf("got %#v, %v; want %s", got, ok, tc.want)
			}
		})
	}
}
