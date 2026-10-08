//go:build windows

package repository

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Windows error codes the Go runtime passes through as a syscall.Errno without
// translating them into a portable one.
const (
	errorSharingViolation = syscall.Errno(32)
	errorLockViolation    = syscall.Errno(33)
	errorInvalidFunction  = syscall.Errno(1)
)

// TestIsRetriableFileErrorOnWindows covers the failures Windows reports
// differently from Unix: a file another process holds open is a sharing
// violation rather than EBUSY, and reading a directory fails with a code of its
// own instead of EISDIR.
func TestIsRetriableFileErrorOnWindows(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "file held open by another process",
			err:  &os.LinkError{Op: "rename", Old: "tmp", New: "metrics.json", Err: errorSharingViolation},
			want: true,
		},
		{
			name: "region of the file locked",
			err:  &fs.PathError{Op: "write", Path: "metrics.json", Err: errorLockViolation},
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

// TestReadOfDirectoryIsRetriedOnWindows pins down a known limitation: reading a
// directory fails with ERROR_INVALID_FUNCTION, which carries no hint that the
// path will never be readable, so the read is repeated on the retry schedule
// before it is reported. The write path, which the storage takes far more
// often, is classified correctly by the shared test.
func TestReadOfDirectoryIsRetriedOnWindows(t *testing.T) {
	_, err := os.ReadFile(t.TempDir())
	if err == nil {
		t.Fatal("reading a directory was expected to fail")
	}

	if !errors.Is(err, errorInvalidFunction) {
		t.Skipf("Windows no longer reports ERROR_INVALID_FUNCTION here: %v", err)
	}
	if !isRetriableFileError(err) {
		t.Fatalf("isRetriableFileError(%v) = false, want true", err)
	}
}

// TestSnapshotPathUnderAFileIsPermanentOnWindows checks the classification of a
// snapshot path whose parent is a regular file, which Windows reports as a
// missing path rather than ENOTDIR.
func TestSnapshotPathUnderAFileIsPermanentOnWindows(t *testing.T) {
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
