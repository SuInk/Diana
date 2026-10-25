// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
)

// A blinking text caret is the one change sameScene forgives: a thin vertical
// strip in a single place. Anything wider (a typed character, a toggled
// checkbox, a moved selection) still counts as a new scene.
const (
	caretMaxWidth  = 4
	caretMaxHeight = 40
	// pixelTolerance absorbs PNG/antialiasing noise per 8-bit channel.
	pixelTolerance = 24
)

// sameScene reports whether two base64 PNG window captures show the same
// scene. Byte-identical captures match without decoding. Otherwise the images
// must have the same size and differ only inside a caret-sized box, because
// text fields blink their caret between the observation and the action, and an
// exact match would make every text field impossible to type into.
func sameScene(before, after string) bool {
	if before == after {
		return true
	}
	left, ok := decodeScene(before)
	if !ok {
		return false
	}
	right, ok := decodeScene(after)
	if !ok || left.Bounds() != right.Bounds() {
		return false
	}
	bounds := left.Bounds()
	minX, minY, maxX, maxY := bounds.Max.X, bounds.Max.Y, bounds.Min.X-1, bounds.Min.Y-1
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if !similarPixel(left, right, x, y) {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x), max(maxY, y)
				// Bail out as soon as the changed area outgrows a caret.
				if maxX-minX+1 > caretMaxWidth || maxY-minY+1 > caretMaxHeight {
					return false
				}
			}
		}
	}
	return true
}

func decodeScene(data string) (image.Image, bool) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, false
	}
	img, err := png.Decode(bytes.NewReader(raw))
	return img, err == nil
}

func similarPixel(left, right image.Image, x, y int) bool {
	lr, lg, lb, la := left.At(x, y).RGBA()
	rr, rg, rb, ra := right.At(x, y).RGBA()
	return channelClose(lr, rr) && channelClose(lg, rg) && channelClose(lb, rb) && channelClose(la, ra)
}

func channelClose(a, b uint32) bool {
	// RGBA() is 16-bit; compare on the 8-bit scale the tolerance is written in.
	a, b = a>>8, b>>8
	if a > b {
		return a-b <= pixelTolerance
	}
	return b-a <= pixelTolerance
}
