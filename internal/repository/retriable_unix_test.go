//go:build !windows

package repository

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestIsRetriableFileErrorOnUnix covers the failures Unix reports differently
// from Windows: reading a directory yields EISDIR, and a file being written by
// another process yields EBUSY or EAGAIN rather than a sharing violation.
func TestIsRetriableFileErrorOnUnix(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "device busy",
			err:  &fs.PathError{Op: "open", Path: "metrics.json", Err: syscall.EBUSY},
			want: true,
		},
		{
			name: "resource temporarily unavailable",
			err:  &fs.PathError{Op: "open", Path: "metrics.json", Err: syscall.EAGAIN},
			want: true,
		},
		{
			name: "too many open files",
			err:  &fs.PathError{Op: "open", Path: "metrics.json", Err: syscall.EMFILE},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetriableFileError(tt.err); got != tt.want {
				t.Fatalf("isRetriableFileError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestReadOfDirectoryIsPermanentOnUnix verifies the case that diverges from
// Windows: Unix reports EISDIR, which says plainly that no repetition will make
// the path readable.
func TestReadOfDirectoryIsPermanentOnUnix(t *testing.T) {
	_, err := os.ReadFile(t.TempDir())
	if err == nil {
		t.Fatal("reading a directory was expected to fail")
	}
	if isRetriableFileError(err) {
		t.Fatalf("isRetriableFileError(%v) = true, want false", err)
	}
}

// TestSnapshotPathUnderAFileIsPermanentOnUnix checks the classification of a
// snapshot path whose parent is a regular file, which Unix reports as ENOTDIR.
func TestSnapshotPathUnderAFileIsPermanentOnUnix(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(file, []byte("[]"), 0644); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	err := os.WriteFile(filepath.Join(file, "metrics.json"), []byte("[]"), 0644)
	if err == nil {
		t.Fatal("writing under a regular file was expected to fail")
	}
	if isRetriableFileError(err) {
		t.Fatalf("isRetriableFileError(%v) = true, want false", err)
	}
}
