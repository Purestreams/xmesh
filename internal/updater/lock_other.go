//go:build !linux

package updater

import "errors"

func lock(string) (func(), error) { return nil, errors.New("updater requires Linux") }
