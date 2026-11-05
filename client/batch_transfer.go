package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

const maxBatchFiles = 100

type batchResult struct {
	succeeded, failed, skipped int
	cancelled                  bool
	firstError                 string
}

func runBatchUpload(ctx context.Context, files []string, upload func(string, func(int64)) error, progress func(int, int, string, int64)) batchResult {
	result := batchResult{}
	for i, file := range files {
		if ctx.Err() != nil {
			result.cancelled = true
			result.skipped = len(files) - i
			break
		}
		err := upload(file, func(read int64) {
			if progress != nil {
				progress(i+1, len(files), filepath.Base(file), read)
			}
		})
		if err == nil {
			result.succeeded++
			continue
		}
		if !errors.Is(err, context.Canceled) {
			result.failed++
			if result.firstError == "" {
				result.firstError = filepath.Base(file) + "：" + transferErrorText(err)
			}
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			result.cancelled = true
			result.skipped = len(files) - i - 1
			break
		}
	}
	return result
}
func (r batchResult) status() string {
	prefix := "批量传输完成"
	if r.cancelled {
		prefix = "批量传输已取消"
	}
	status := fmt.Sprintf("%s：成功 %d，失败 %d，未处理 %d", prefix, r.succeeded, r.failed, r.skipped)
	if r.firstError != "" {
		status += "；" + r.firstError
	}
	return status
}
