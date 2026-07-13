//go:build windows

package core

import (
	"fmt"
	"os"
)

// Windows ACL ownership checks are outside the Unix deployment scope for this
// extension. Fail closed rather than accepting a path whose replacement
// authority cannot be proven.
func validateDirectExecutableOwner(_ os.FileInfo, subject string) error {
	return fmt.Errorf("cannot determine %s owner on Windows", subject)
}
