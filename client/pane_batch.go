package main

import (
	"context"
	"errors"
	"fmt"
)

type paneBatchResult struct {
	succeeded, failed, skipped, partial int
	cancelled                           bool
	cancelledName                       string
	errors                              []string
}

func runPaneBatch(ctx context.Context, items []paneCopyItem, copyItem func(paneCopyItem, func(int64)) (bool, error), progress func(int, int, string, int64)) paneBatchResult {
	var result paneBatchResult
	for i, item := range items {
		if ctx.Err() != nil {
			result.cancelled, result.skipped = true, len(items)-i
			break
		}
		partial, err := copyItem(item, func(read int64) {
			if progress != nil {
				progress(i+1, len(items), item.name, read)
			}
		})
		if partial {
			result.partial++
		}
		if err == nil {
			result.succeeded++
			continue
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			result.cancelled, result.cancelledName, result.skipped = true, item.name, len(items)-i-1
			if !errors.Is(err, context.Canceled) {
				result.failed++
				result.errors = append(result.errors, item.name+"："+transferErrorText(err))
			}
			break
		}
		result.failed++
		message := item.name + "：" + transferErrorText(err)
		if partial {
			message += "（目标保留部分内容）"
		}
		result.errors = append(result.errors, message)
	}
	return result
}

func (r paneBatchResult) status() string {
	prefix := "批量传输完成"
	if r.cancelled {
		prefix = "批量传输已取消"
	}
	status := fmt.Sprintf("%s：成功 %d，失败 %d，未处理 %d", prefix, r.succeeded, r.failed, r.skipped)
	if r.cancelledName != "" {
		status += "；已取消：" + r.cancelledName
	}
	if r.partial > 0 {
		status += fmt.Sprintf("；%d 个目录保留部分内容", r.partial)
	}
	if len(r.errors) > 0 {
		status += "；" + r.errors[0]
	}
	return status
}
