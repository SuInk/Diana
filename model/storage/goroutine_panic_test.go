// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"testing"

	"github.com/SuInk/diana/internal/safego/safegotest"
)

func TestRecoverGoroutinePanicRecovers(t *testing.T) {
	safegotest.AssertRecovers(t, recoverGoroutinePanic, "probe", "storage.probe")
}
