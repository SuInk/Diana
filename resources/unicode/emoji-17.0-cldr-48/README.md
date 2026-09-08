# Emoji Names: Unicode 17.0 / CLDR 48

Official source snapshots downloaded on 2026-09-08. The parent Go package embeds
these files for local emoji-name lookup. Diana sends only names for emoji found
in model input, not the complete table. Input text and stored messages stay intact.

| File | Source |
| --- | --- |
| emoji-test.txt | https://www.unicode.org/Public/17.0.0/emoji/emoji-test.txt |
| annotations-zh.xml | https://raw.githubusercontent.com/unicode-org/cldr/release-48/common/annotations/zh.xml |
| annotations-derived-zh.xml | https://raw.githubusercontent.com/unicode-org/cldr/release-48/common/annotationsDerived/zh.xml |
| LICENSE | https://raw.githubusercontent.com/unicode-org/cldr/release-48/LICENSE |

The emoji test file supplies English emoji names and qualification variants.
In the CLDR XML files, `annotation` elements with `type="tts"` supply display
names; other annotations supply keywords. Preserve full emoji sequences,
including variation selectors, skin tones, and zero-width joiners, when
building a lookup. CLDR entries may carry draft status; the source XML retains
that status for inspection. Names and keywords describe symbols, not a user's intent.

Verification: both XML files pass `xmllint --noout --nonet`. U+1FAEA is
`distorted face` in emoji-test.txt and has a Simplified Chinese name in
annotations-zh.xml (marked `draft="contributed"`). U+1F48A is the separate
`pill` emoji.
