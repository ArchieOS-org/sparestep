package recovery

import (
	"os"
	"path/filepath"
)

func openDirectory(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if _, _, err := directoryIdentityFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func directoryIdentityFile(file *os.File) (uint64, uint64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, 0, err
	}
	return identityFromInfo(info)
}

func renameNoReplace(from, to string) error {
	fromDir, err := openDirectory(filepath.Dir(from))
	if err != nil {
		return err
	}
	defer fromDir.Close()
	toDir, err := openDirectory(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer toDir.Close()
	return renameNoReplaceAt(fromDir, filepath.Base(from), toDir, filepath.Base(to))
}
