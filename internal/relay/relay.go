package relay

import (
	"net"
	"sync"
	"time"
)

type closeWriter interface{ CloseWrite() error }

type Options struct {
	WriteStallTimeout  time.Duration
	OnLeftToRightWrite func(time.Duration)
	OnRightToLeftWrite func(time.Duration)
}

func Bidirectional(left, right net.Conn, onLeftToRight, onRightToLeft func(int), options Options) {
	var wg sync.WaitGroup
	wg.Add(2)
	copyOne := func(dst, src net.Conn, count func(int), observe func(time.Duration)) {
		defer wg.Done()
		buffer := make([]byte, 32<<10)
		for {
			n, err := src.Read(buffer)
			if n > 0 {
				if options.WriteStallTimeout > 0 {
					_ = dst.SetWriteDeadline(time.Now().Add(options.WriteStallTimeout))
				}
				started := time.Now()
				written := 0
				for written < n {
					m, writeErr := dst.Write(buffer[written:n])
					written += m
					if m > 0 && options.WriteStallTimeout > 0 {
						_ = dst.SetWriteDeadline(time.Now().Add(options.WriteStallTimeout))
					}
					if writeErr != nil || m == 0 {
						if observe != nil {
							observe(time.Since(started))
						}
						_ = left.Close()
						_ = right.Close()
						return
					}
				}
				_ = dst.SetWriteDeadline(time.Time{})
				if observe != nil {
					observe(time.Since(started))
				}
				if count != nil {
					count(written)
				}
			}
			if err != nil {
				if writer, ok := dst.(closeWriter); ok {
					_ = writer.CloseWrite()
				}
				return
			}
		}
	}
	go copyOne(right, left, onLeftToRight, options.OnLeftToRightWrite)
	go copyOne(left, right, onRightToLeft, options.OnRightToLeftWrite)
	wg.Wait()
	_ = left.Close()
	_ = right.Close()
}
