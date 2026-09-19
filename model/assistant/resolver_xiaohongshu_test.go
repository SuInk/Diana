package assistant

import "testing"

func TestXiaohongshuPageURLRestoresLoginRedirectTarget(t *testing.T) {
	got := xiaohongshuPageURL("https://www.xiaohongshu.com/login?redirectPath=http%3A%2F%2Fwww.xiaohongshu.com%2Fdiscovery%2Fitem%2Fnote-id%3Fxsec_source%3Dapp_share%26xsec_token%3Dtoken")
	want := "https://www.xiaohongshu.com/discovery/item/note-id?xsec_source=app_share&xsec_token=token"
	if got != want {
		t.Fatalf("xiaohongshuPageURL() = %q, want %q", got, want)
	}
}

func TestXiaohongshuPageURLRejectsExternalLoginRedirect(t *testing.T) {
	raw := "https://www.xiaohongshu.com/login?redirectPath=https%3A%2F%2Fexample.com%2Fnote"
	if got := xiaohongshuPageURL(raw); got != raw {
		t.Fatalf("xiaohongshuPageURL() = %q, want original URL", got)
	}
}

func TestXiaohongshuPageURLUpgradesNoteHTTPURL(t *testing.T) {
	got := xiaohongshuPageURL("http://www.xiaohongshu.com/explore/note-id?xsec_source=app_share")
	want := "https://www.xiaohongshu.com/explore/note-id?xsec_source=app_share"
	if got != want {
		t.Fatalf("xiaohongshuPageURL() = %q, want %q", got, want)
	}
}
