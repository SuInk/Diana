package assistant

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestParseYTDLPExtractorsPreservesFullListAndBrokenMarkers(t *testing.T) {
	got := parseYTDLPExtractors("youtube\r\nyoutube:tab\nBiliBili\nTwitter (CURRENTLY BROKEN)\nyoutube\n\n")
	want := []string{"BiliBili", "Twitter (CURRENTLY BROKEN)", "youtube", "youtube:tab"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestYTDLPSupportedExtractorsUsesOnlySafeListingFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$*\" = \"--ignore-config --no-plugin-dirs --list-extractors\" ] || exit 1\nprintf 'youtube\\nBiliBili\\nyoutube:tab\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "yt-dlp"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, err := YTDLPSupportedExtractors(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"BiliBili", "youtube", "youtube:tab"}) {
		t.Fatalf("got=%v err=%v", got, err)
	}
}
