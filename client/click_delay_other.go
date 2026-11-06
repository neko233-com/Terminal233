//go:build !windows

package main

import "time"

func hostInspectorDelay() time.Duration { return 525 * time.Millisecond }
