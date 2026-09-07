package assistant

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
)

func TestAvatarSimilarityMatchesResizedCompressedCopy(t *testing.T) {
	source := patternedAvatar(384)
	candidate := patternedAvatar(640)
	unrelated := unrelatedAvatar(640)

	var sourcePNG, candidateJPEG, unrelatedJPEG bytes.Buffer
	if err := png.Encode(&sourcePNG, source); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&candidateJPEG, candidate, &jpeg.Options{Quality: 82}); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&unrelatedJPEG, unrelated, &jpeg.Options{Quality: 82}); err != nil {
		t.Fatal(err)
	}
	sourceHash, err := avatarFingerprint(sourcePNG.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	candidateHash, _ := avatarFingerprint(candidateJPEG.Bytes())
	unrelatedHash, _ := avatarFingerprint(unrelatedJPEG.Bytes())
	matchScore := avatarSimilarity(sourceHash, candidateHash)
	unrelatedScore := avatarSimilarity(sourceHash, unrelatedHash)
	if matchScore < avatarMatchMinimumScore || matchScore-unrelatedScore < avatarMatchMinimumLead {
		t.Fatalf("match=%.4f unrelated=%.4f", matchScore, unrelatedScore)
	}
}

func TestNewImageEvidencePromotesLowValueDuplicate(t *testing.T) {
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "group", UserID: "user", MessageID: "current", Time: 200,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{imageContentSHA256Key: "new-image"}}},
	}
	history := []MessageEvent{{
		Kind: EventKindGroup, GroupID: "group", UserID: "bot", MessageID: "answer", Time: 190, Outbound: true,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{imageContentSHA256Key: "old-image"}}},
	}}
	if !imageEvidenceNewSinceLastBot(event, history, "bot") {
		t.Fatal("different current image was not treated as new evidence")
	}
	decision := proactiveReplyDecision{
		ShouldReply: false, Confidence: 0.98, Category: "none", Answerable: true,
		RequestsResponse: true, Blocker: proactiveBlockerLowValue, Reason: "duplicate",
	}
	if !promoteNewImageEvidence(&decision, event, true, 0.5, chatInSettings{}) {
		t.Fatal("new image evidence did not override the low-value duplicate decision")
	}
	if !decision.ShouldReply || decision.Category != "needs_response" || decision.TargetMessageID != event.MessageID {
		t.Fatalf("decision=%#v", decision)
	}

	history[0].Segments[0].Data[imageContentSHA256Key] = "new-image"
	if imageEvidenceNewSinceLastBot(event, history, "bot") {
		t.Fatal("the same previously answered image was treated as new evidence")
	}
}

func TestLiveAvatarSimilarity(t *testing.T) {
	sourcePath := os.Getenv("DIANA_LIVE_AVATAR_SOURCE")
	candidatePath := os.Getenv("DIANA_LIVE_AVATAR_CANDIDATE")
	if sourcePath == "" || candidatePath == "" {
		t.Skip("DIANA_LIVE_AVATAR_SOURCE and DIANA_LIVE_AVATAR_CANDIDATE are required")
	}
	sourceBody, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	candidateBody, err := os.ReadFile(candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	source, err := avatarFingerprint(sourceBody)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := avatarFingerprint(candidateBody)
	if err != nil {
		t.Fatal(err)
	}
	score := avatarSimilarity(source, candidate)
	t.Logf("avatar similarity %.6f", score)
	if score < avatarMatchMinimumScore {
		t.Fatalf("score %.6f below threshold %.2f", score, avatarMatchMinimumScore)
	}
}

func patternedAvatar(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			nx, ny := float64(x)/float64(size), float64(y)/float64(size)
			value := uint8(225)
			switch {
			case (nx-0.32)*(nx-0.32)+(ny-0.42)*(ny-0.42) < 0.035:
				value = 55
			case (nx-0.68)*(nx-0.68)+(ny-0.42)*(ny-0.42) < 0.035:
				value = 105
			case ny > 0.66 && nx > 0.25 && nx < 0.75:
				value = 145
			}
			img.SetRGBA(x, y, color.RGBA{R: value, G: uint8(min(255, int(value)+8)), B: uint8(max(0, int(value)-5)), A: 255})
		}
	}
	return img
}

func unrelatedAvatar(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			value := uint8((x/24+y/24)%2) * 210
			img.SetRGBA(x, y, color.RGBA{R: value, G: 40, B: 220 - value/2, A: 255})
		}
	}
	return img
}
