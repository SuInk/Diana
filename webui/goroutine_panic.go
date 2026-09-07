// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import "github.com/SuInk/diana/internal/safego"

func recoverGoroutinePanic(component string) { safego.Recover("webui." + component) }
