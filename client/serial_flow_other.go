//go:build !windows

package main

import (
	"errors"
	"io"
)

func openSerialFlowPort(serialSettings) (io.ReadWriteCloser, error) {
	return nil, errors.New("当前系统暂不支持串口流控")
}
