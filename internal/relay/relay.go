package relay

import (
	"io"
	"net"
	"sync"
)

type closeWriter interface{ CloseWrite() error }

func Bidirectional(left, right net.Conn, onLeftToRight, onRightToLeft func(int)) {
	var wg sync.WaitGroup
	wg.Add(2)
	copyOne := func(dst, src net.Conn, count func(int)) {
		defer wg.Done()
		buffer := make([]byte, 32<<10)
		for {
			n, err := src.Read(buffer)
			if n > 0 {
				if count != nil {
					count(n)
				}
				if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
					return
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
	go copyOne(right, left, onLeftToRight)
	go copyOne(left, right, onRightToLeft)
	wg.Wait()
	_ = left.Close()
	_ = right.Close()
}

var _ = io.EOF
