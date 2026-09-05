package webui

import "testing"

func TestReleaseAssetPrefersCurrentNameAndFallsBackToLegacy(t *testing.T) {
	current := ReleaseAsset{Name: "diana-linux-amd64.tar.gz", URL: "https://example.test/new"}
	legacy := ReleaseAsset{Name: "diana-webui-linux-amd64.tar.gz", URL: "https://example.test/old"}
	for _, tc := range []struct {
		name   string
		assets []ReleaseAsset
		want   string
	}{
		{"both", []ReleaseAsset{legacy, current}, current.Name},
		{"new only", []ReleaseAsset{current}, current.Name},
		{"old only", []ReleaseAsset{legacy}, legacy.Name},
		{"wrong platform", []ReleaseAsset{{Name: "diana-webui-windows-amd64.zip", URL: legacy.URL}}, ""},
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

func TestReleaseAssetMacOSFallsBackToDarwinArchive(t *testing.T) {
	legacy := ReleaseAsset{Name: "diana-webui-darwin-arm64.tar.gz", URL: "https://example.test/legacy"}
	got, ok := (ReleaseEntry{Assets: []ReleaseAsset{legacy}}).asset("diana-macos-arm64.tar.gz")
	if !ok || got != legacy {
		t.Fatalf("macOS legacy lookup = %#v, %v", got, ok)
	}
}
