//go:build !windows

package core

import (
	"fmt"
	"os"
	"syscall"
)

func validateDirectExecutableOwner(info os.FileInfo, subject string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine %s owner", subject)
	}
	return validateDirectOwnerID(stat.Uid, uint32(os.Geteuid()), subject)
}
