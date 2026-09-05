// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"strings"
)

var errMediaIdentityMismatch = errors.New("media content does not match platform MD5")

type videoMediaIdentitiesKey struct{}

func withVideoMediaIdentities(ctx context.Context, platform string, groups ...[]MessageSegment) context.Context {
	if platform != PlatformOneBotV11 {
		return ctx
	}
	identities := make(map[string]string)
	for _, segments := range groups {
		for _, segment := range segments {
			for _, source := range videoSourceCandidates([]MessageSegment{segment}) {
				digest := explicitMediaMD5(segment.Data)
				if previous, exists := identities[source]; exists && previous != digest {
					digest = ""
				}
				identities[source] = digest
			}
		}
	}
	return context.WithValue(ctx, videoMediaIdentitiesKey{}, identities)
}

// Opaque OneBot file IDs and filenames are not globally unique. Only explicit
// digest fields qualify, and a new mapping is written after checking the bytes.
func platformImageMD5(platform string, segment MessageSegment) string {
	if platform != PlatformOneBotV11 || segment.Type != "image" {
		return ""
	}
	return explicitMediaMD5(segment.Data)
}

func explicitMediaMD5(data map[string]string) string {
	for _, key := range []string{"md5", "file_md5", "fileMd5"} {
		value := strings.ToLower(strings.TrimSpace(data[key]))
		if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == md5.Size {
			return value
		}
	}
	return ""
}

func readPlatformHistoryImage(ctx context.Context, platform string, segment MessageSegment, source string) ([]byte, string, error) {
	digest := platformImageMD5(platform, segment)
	if digest == "" {
		return readHistoryImageSource(ctx, source, historyMediaReadyTimeout)
	}
	dir, err := historyMediaDir()
	if err != nil {
		return nil, "", err
	}
	path, err := fetchMediaContent(ctx, dir, "onebot-image-md5:"+digest, maxHistoryImageBytes, func(ctx context.Context) ([]byte, string, string, error) {
		body, mime, err := readHistoryImageSource(ctx, source, historyMediaReadyTimeout)
		if err != nil {
			return nil, "", "", err
		}
		sum := md5.Sum(body)
		if hex.EncodeToString(sum[:]) != digest {
			return nil, "", "", errMediaIdentityMismatch
		}
		return body, mime, "", nil
	})
	if errors.Is(err, errMediaIdentityMismatch) {
		// The URL cache already holds these bytes. Keep usable media, but never
		// publish a false platform identity or share another caller's fallback.
		return readHistoryImageSource(ctx, source, historyMediaReadyTimeout)
	}
	if err != nil {
		return nil, "", err
	}
	return readHistoryImageSource(ctx, path, 0)
}
