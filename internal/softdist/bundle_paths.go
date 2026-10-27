package softdist

import (
	"fmt"
	"os"
	"path/filepath"
)

func bundleNoLinks(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("package path contains a link")
			}
			if err = bundleNoReparse(p); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
