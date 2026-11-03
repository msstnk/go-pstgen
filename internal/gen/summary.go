package gen

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/msstnk/go-pstgen/pst"
)

// CheckSummaryPath returns an error if path already exists, so a run can fail before anything is generated.
func CheckSummaryPath(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("summary file %s already exists", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// WriteSummary reads back each PST in files and writes the number of messages per folder to path as TSV, one
// "file<TAB>folder<TAB>count" line per folder (folders without messages are left out). A split file (-2.pst, ...) is
// listed on its own. It refuses to overwrite an existing file; nothing is written if any PST cannot be read.
func WriteSummary(path string, files []string) error {
	var rows []string
	for _, file := range files {
		counts, err := pst.CountItems(file)
		if err != nil {
			return err
		}
		for _, c := range counts {
			rows = append(rows, c.TSV(file)+"\n")
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("summary file %s already exists", path)
		}
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range rows {
		w.WriteString(r)
	}
	err = w.Flush()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path) // a partial file would make a retry fail with "already exists"
	}
	return err
}
