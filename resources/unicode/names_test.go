package emojinames

import "testing"

func TestOfficialNamesAndFullSequences(t *testing.T) {
	for _, tt := range []struct{ text, emoji, english, chinese string }{
		{"🫪🫪", "🫪", "distorted face", "变形的脸"},
		{"💊", "💊", "pill", "药丸"},
		{"👩🏽‍💻", "👩🏽‍💻", "woman technologist: medium skin tone", ""},
		{"🇨🇳", "🇨🇳", "flag: China", "旗: 中国"},
		{"❤️", "❤️", "red heart", "红心"},
		{"❤", "❤", "red heart", "红心"},
		{"1️⃣", "1️⃣", "keycap: 1", ""},
	} {
		t.Run(tt.english+tt.emoji, func(t *testing.T) {
			names, err := Find(tt.text, 32)
			if err != nil {
				t.Fatal(err)
			}
			if len(names) != 1 || names[0].Emoji != tt.emoji || names[0].English != tt.english || names[0].Chinese == "" {
				t.Fatalf("Find(%q) = %+v", tt.text, names)
			}
			if tt.chinese != "" && names[0].Chinese != tt.chinese {
				t.Fatalf("Chinese = %q, want %q", names[0].Chinese, tt.chinese)
			}
		})
	}
	names, err := Find("文字 ABC 123 # *", 32)
	if err != nil || len(names) != 0 {
		t.Fatalf("plain text matched: %+v, %v", names, err)
	}
	names, err = Find("🫪💊❤️", 2)
	if err != nil || len(names) != 2 {
		t.Fatalf("limit not respected: %+v, %v", names, err)
	}
}
