package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrMetaConflict reports that a rename's destination metadata file already
// holds accounts the source does not explain.
var ErrMetaConflict = errors.New("account: rename destination metadata exists")

// ReadMetaIDs lists the account ids of provider in the metadata file at
// path. Unlike Load it does not reject records of another provider: a file
// shared through accountsPath can hold both sides of a rename.
func ReadMetaIDs(path string, provider ProviderID) ([]AccountID, error) {
	file, ok, err := readMetaFile(path)
	if err != nil || !ok {
		return nil, err
	}
	var out []AccountID
	for _, rec := range file.Accounts {
		if rec.Provider == string(provider) {
			out = append(out, AccountID(rec.ID))
		}
	}
	return out, nil
}

// RenameMeta moves the records of provider from to provider to, rewriting
// ids through moves (ids missing from moves keep their text), from the
// metadata file at srcPath to the one at dstPath. It reports whether
// anything changed. A crash between writing dstPath and removing srcPath is
// finished on the next call, which sees every moved id already at dstPath.
func RenameMeta(srcPath, dstPath string, from, to ProviderID, moves map[AccountID]AccountID) (bool, error) {
	src, srcOK, err := readMetaFile(srcPath)
	if err != nil || !srcOK {
		return false, err
	}
	if srcPath == dstPath {
		changed := false
		for i := range src.Accounts {
			if renameRecord(&src.Accounts[i], from, to, moves) {
				changed = true
			}
		}
		if !changed {
			return false, nil
		}
		return true, (&Repository{path: dstPath}).write(src)
	}
	dst, dstOK, err := readMetaFile(dstPath)
	if err != nil {
		return false, err
	}
	if dstOK {
		have := make(map[string]bool, len(dst.Accounts))
		for _, rec := range dst.Accounts {
			have[rec.ID] = rec.Provider == string(to)
		}
		for _, rec := range src.Accounts {
			renameRecord(&rec, from, to, moves)
			if !have[rec.ID] {
				return false, fmt.Errorf("%w: %s", ErrMetaConflict, dstPath)
			}
		}
	} else {
		for i := range src.Accounts {
			renameRecord(&src.Accounts[i], from, to, moves)
		}
		if err := (&Repository{path: dstPath}).write(src); err != nil {
			return false, err
		}
	}
	if err := os.Remove(srcPath); err != nil && !os.IsNotExist(err) {
		return true, fmt.Errorf("accounts %s: %w", srcPath, err)
	}
	if handle, err := os.Open(filepath.Dir(srcPath)); err == nil {
		handle.Sync()
		handle.Close()
	}
	return true, nil
}

func renameRecord(rec *metaRecord, from, to ProviderID, moves map[AccountID]AccountID) bool {
	if rec.Provider != string(from) {
		return false
	}
	rec.Provider = string(to)
	if next, ok := moves[AccountID(rec.ID)]; ok {
		rec.ID = string(next)
	}
	return true
}

func readMetaFile(path string) (metaFile, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return metaFile{}, false, nil
	}
	if err != nil {
		return metaFile{}, false, fmt.Errorf("accounts %s: %w", path, err)
	}
	var file metaFile
	if err := json.Unmarshal(b, &file); err != nil {
		return metaFile{}, false, fmt.Errorf("accounts %s: %w", path, err)
	}
	if file.Version != metaSchemaVersion {
		return metaFile{}, false, fmt.Errorf("accounts %s: unsupported version %d", path, file.Version)
	}
	return file, true, nil
}
