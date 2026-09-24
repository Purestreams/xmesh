package atomicfile

import (
	"os"
	"runtime"
	"time"
)

// Replace keeps the previous file intact if Windows temporarily denies a rename.
func Replace(source, target string) error {
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		err = os.Rename(source, target)
		if err == nil || runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return err
}
