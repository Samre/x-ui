// +build linux

package sys

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"x-ui/logger"
)

func getLinesNum(filename string) (int, error) {
	file, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	sum := 0
	buf := make([]byte, 8192)
	// buffPosition 必须跨读循环保留：每次 Read 只覆盖 buf 的前 n 个字节，
	// 若每轮从 0 重扫，上一块里的换行会被反复计数。
	buffPosition := 0
	for {
		n, err := file.Read(buf)

		// 只在本次读到的 [start, n) 里找换行，不把上一块的残留字节再数一遍
		start := buffPosition
		if start > n {
			start = n
		}
		for {
			i := bytes.IndexByte(buf[start:n], '\n')
			if i < 0 {
				break
			}
			start += i + 1
			buffPosition = start
			sum++
		}

		if err == io.EOF {
			return sum, nil
		} else if err != nil {
			return sum, err
		}
	}
}

func GetTCPCount() (int, error) {
	root := HostProc()

	tcp4, err := getLinesNum(fmt.Sprintf("%v/net/tcp", root))
	if err != nil {
		return tcp4, err
	}
	tcp6, err := getLinesNum(fmt.Sprintf("%v/net/tcp6", root))
	if err != nil {
		// 原来返回 (tcp4+tcp6, nil)：报成功却把 tcp6 当 0，调用方拿到
		// 一个偏小且无声的计数，err 变量也只是被赋值后丢弃。
		logger.Warning("read tcp6 connections failed:", err)
		return tcp4, nil
	}

	return tcp4 + tcp6, nil
}

func GetUDPCount() (int, error) {
	root := HostProc()

	udp4, err := getLinesNum(fmt.Sprintf("%v/net/udp", root))
	if err != nil {
		return udp4, err
	}
	udp6, err := getLinesNum(fmt.Sprintf("%v/net/udp6", root))
	if err != nil {
		logger.Warning("read udp6 connections failed:", err)
		return udp4, nil
	}

	return udp4 + udp6, nil
}
