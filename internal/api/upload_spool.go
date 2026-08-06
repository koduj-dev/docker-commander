package api

import (
	"archive/tar"
	"errors"
	"io"
	"net/http"
	"os"
)

// maxUploadBytes bounds a single file upload into a container or a volume.
//
// Not a security boundary on its own — the caller already needs write access to
// the containers/volumes section — but a ceiling that keeps one request from
// filling the host's disk, and an honest number instead of the previous 4 GiB
// which nobody chose deliberately.
const maxUploadBytes = 2 << 30 // 2 GiB

// errUploadTooBig is returned when the body exceeds maxUploadBytes.
var errUploadTooBig = errors.New("upload exceeds the size limit")

// spoolUpload streams a request body to a temporary file and wraps it in a tar
// stream, ready to hand to the Docker copy API.
//
// The tar header needs the size up front, which is why the body used to be read
// into memory in full — with a 4 GiB ceiling, so one request could drive the
// server's RSS to several gigabytes (io.ReadAll's doubling makes the peak roughly
// twice the payload) and a handful of concurrent ones could OOM-kill the process
// on a normal host. Spooling to disk answers the same question — how big is it —
// without holding it.
//
// Returns a reader the caller must Close, which also removes the temp file.
func spoolUpload(w http.ResponseWriter, r *http.Request, name string) (io.ReadCloser, int64, error) {
	f, err := os.CreateTemp("", "dc-upload-*")
	if err != nil {
		return nil, 0, err
	}
	// Unlinked immediately: the descriptor keeps the data reachable, and nothing
	// is left behind if the process dies mid-upload.
	_ = os.Remove(f.Name())

	// A real upload can run for minutes, so it trades the whole-request deadline
	// for a rolling one; see streamingBody.
	body := http.MaxBytesReader(w, streamingBody(w, r), maxUploadBytes)
	size, err := io.Copy(f, body)
	if err != nil {
		f.Close()
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, 0, errUploadTooBig
		}
		return nil, 0, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, 0, err
	}

	// The tar is produced lazily through a pipe so neither the payload nor the
	// archive around it is ever held whole.
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: size})
		if err == nil {
			_, err = io.Copy(tw, f)
		}
		if err == nil {
			err = tw.Close()
		}
		f.Close()
		_ = pw.CloseWithError(err)
	}()
	return pr, size, nil
}
