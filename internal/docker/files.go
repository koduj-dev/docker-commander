package docker

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// maxExtractBytes caps both the buffered .zip upload (it needs random access)
// and the total uncompressed output, so a small archive can't expand into an
// unbounded tar stream (zip-bomb). .tar/.tar.gz stream straight through.
const maxExtractBytes = 512 << 20 // 512 MiB

// archiveEntryName sanitises a zip entry path: it must be a relative path that
// stays within the destination. Returns the cleaned name and false if the entry
// should be skipped (absolute path, or a ".." escape — zip-slip).
func archiveEntryName(name string) (string, bool) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || path.IsAbs(name) {
		return "", false
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// FileEntry is one item in a container directory listing.
type FileEntry struct {
	Name   string `json:"name"`
	IsDir  bool   `json:"isDir"`
	IsLink bool   `json:"isLink"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	Target string `json:"target,omitempty"` // symlink target
}

// execCapture runs a one-shot command in the container and returns its stdout,
// stderr and exit code. Used for filesystem operations the Docker API does not
// expose directly (listing, deleting). Requires the command to exist in the
// image (e.g. busybox/coreutils `ls`, `rm`).
func execCapture(ctx context.Context, cli *client.Client, id string, cmd []string) (string, string, int, error) {
	created, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd: cmd, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return "", "", 0, err
	}
	att, err := cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", "", 0, err
	}
	defer att.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, att.Reader); err != nil {
		return "", "", 0, err
	}
	insp, err := cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return stdout.String(), stderr.String(), 0, err
	}
	return stdout.String(), stderr.String(), insp.ExitCode, nil
}

// ListPath lists one directory level inside a container by running `ls`. It is
// shell-independent (direct argv) but needs an `ls` binary in the image.
func (m *Manager) ListPath(ctx context.Context, hostID int64, id, path string) ([]FileEntry, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if path == "" {
		path = "/"
	}
	stdout, stderr, code, err := execCapture(ctx, cli, id, []string{"ls", "-lAp", path})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = "cannot list directory (is `ls` present in the image?)"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return parseLsLong(stdout), nil
}

// parseLsLong parses `ls -lAp` output. Columns: mode nlink owner group size
// <date…> name, where -p suffixes directories with '/'. Names with spaces are
// preserved by joining the trailing tokens.
func parseLsLong(out string) []FileEntry {
	var entries []FileEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		mode := fields[0]
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		name := strings.Join(fields[8:], " ")

		e := FileEntry{Mode: mode, Size: size}
		switch mode[0] {
		case 'd':
			e.IsDir = true
		case 'l':
			e.IsLink = true
			if i := strings.Index(name, " -> "); i >= 0 {
				e.Target = name[i+4:]
				name = name[:i]
			}
		}
		// -p appends '/' to directory names.
		if strings.HasSuffix(name, "/") {
			e.IsDir = true
			name = strings.TrimSuffix(name, "/")
		}
		if name == "" {
			continue
		}
		e.Name = name
		entries = append(entries, e)
	}
	return entries
}

// CopyFrom streams a path out of a container as a TAR archive along with its
// stat (so the caller can tell a file from a directory). Caller closes the reader.
func (m *Manager) CopyFrom(ctx context.Context, hostID int64, id, path string) (io.ReadCloser, container.PathStat, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, container.PathStat{}, err
	}
	return cli.CopyFromContainer(ctx, id, path)
}

// CopyTo writes a TAR archive into the container at destDir.
func (m *Manager) CopyTo(ctx context.Context, hostID int64, id, destDir string, content io.Reader) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	return cli.CopyToContainer(ctx, id, destDir, content, container.CopyToContainerOptions{})
}

// MakeDir creates a directory inside the container (mkdir -p).
func (m *Manager) MakeDir(ctx context.Context, hostID int64, id, path string) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	_, stderr, code, err := execCapture(ctx, cli, id, []string{"mkdir", "-p", path})
	if err != nil {
		return err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = "could not create directory"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// UploadExtract streams an archive into destDir, extracting it. The Docker
// CopyToContainer API takes a TAR and untars it (jailing traversal), so we
// convert the upload to a TAR stream based on its extension.
func (m *Manager) UploadExtract(ctx context.Context, hostID int64, id, destDir, filename string, body io.Reader) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".tar"):
		return cli.CopyToContainer(ctx, id, destDir, body, container.CopyToContainerOptions{})

	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		gz, err := gzip.NewReader(body)
		if err != nil {
			return fmt.Errorf("not a valid gzip archive: %w", err)
		}
		defer gz.Close()
		return cli.CopyToContainer(ctx, id, destDir, gz, container.CopyToContainerOptions{})

	case strings.HasSuffix(lower, ".zip"):
		data, err := io.ReadAll(io.LimitReader(body, maxExtractBytes))
		if err != nil {
			return err
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return fmt.Errorf("not a valid .zip archive: %w", err)
		}
		// Convert the zip to a TAR stream on the fly and hand it to the daemon.
		pr, pw := io.Pipe()
		go func() {
			tw := tar.NewWriter(pw)
			var written int64
			for _, f := range zr.File {
				name, ok := archiveEntryName(f.Name)
				if !ok {
					continue // zip-slip / absolute path
				}
				fi := f.FileInfo()
				// Only regular files and directories — skip symlinks, devices, etc.
				if !fi.IsDir() && !fi.Mode().IsRegular() {
					continue
				}
				if !fi.IsDir() {
					if written += int64(f.UncompressedSize64); written > maxExtractBytes {
						pw.CloseWithError(errors.New("archive expands beyond the size limit (possible zip bomb)"))
						return
					}
				}
				hdr, err := tar.FileInfoHeader(fi, "")
				if err != nil {
					pw.CloseWithError(err)
					return
				}
				hdr.Name = name // cleaned relative path
				if err := tw.WriteHeader(hdr); err != nil {
					pw.CloseWithError(err)
					return
				}
				if !fi.IsDir() {
					rc, err := f.Open()
					if err != nil {
						pw.CloseWithError(err)
						return
					}
					_, err = io.Copy(tw, rc)
					rc.Close()
					if err != nil {
						pw.CloseWithError(err)
						return
					}
				}
			}
			pw.CloseWithError(tw.Close())
		}()
		return cli.CopyToContainer(ctx, id, destDir, pr, container.CopyToContainerOptions{})

	default:
		return fmt.Errorf("unsupported archive (use .zip, .tar, .tar.gz or .tgz)")
	}
}

// DeletePath removes a path inside the container (rm -rf).
func (m *Manager) DeletePath(ctx context.Context, hostID int64, id, path string) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	_, stderr, code, err := execCapture(ctx, cli, id, []string{"rm", "-rf", path})
	if err != nil {
		return err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = "delete failed"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
