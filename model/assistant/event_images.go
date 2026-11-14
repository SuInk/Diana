// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

func eventCarriesImages(event MessageEvent) bool {
	if hasImageSegment(event.Segments) {
		return true
	}
	return event.Quoted != nil && hasImageSegment(event.Quoted.Segments)
}
