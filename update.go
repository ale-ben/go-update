// Package update provides tooling to auto-update binary releases
// from GitHub based on the user's current version and operating system.
package update

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"crypto/sha256"
	"github.com/apex/log"
	"github.com/c4milo/unpackit"
	"github.com/pkg/errors"
)

// Proxy is used to proxy a reader, for example
// using https://github.com/cheggaaa/pb to provide
// progress updates.
type Proxy func(int, io.ReadCloser) io.ReadCloser

// NopProxy does nothing.
var NopProxy = func(size int, r io.ReadCloser) io.ReadCloser {
	return r
}

// Manager is the update manager.
type Manager struct {
	Store           // Store for releases such as Github or a custom private store.
	Command  string // Command is the executable's name.
	Checksum string // Checksum is the checksum of the file
}

// Release represents a project release.
type Release struct {
	Version     string    // Version is the release version.
	Notes       string    // Notes is the markdown release notes.
	URL         string    // URL is the notes url.
	PublishedAt time.Time // PublishedAt is the publish time.
	Assets      []*Asset  // Assets is the release assets.
}

// Asset represents a project release asset.
type Asset struct {
	Name      string // Name of the asset.
	Size      int    // Size of the asset.
	URL       string // URL of the asset.
	Downloads int    // Downloads count.
}

// InstallTo binary to the given dir.
func (m *Manager) InstallTo(path, dir string) error {
	log.Debugf("unpacking %q", path)

	f, err := os.Open(path)
	if err != nil {
		return errors.Wrap(err, "opening tarball")
	}

	tmpdir, err := unpackit.Unpack(f, "")
	if err != nil {
		f.Close()
		return errors.Wrap(err, "unpacking tarball")
	}

	if err := f.Close(); err != nil {
		return errors.Wrap(err, "closing tarball")
	}

	bin := filepath.Join(tmpdir, m.Command)

	if err := os.Chmod(bin, 0755); err != nil {
		return errors.Wrap(err, "chmod")
	}

	// If checksum is initialized check the file
	if m.Checksum != "" {
		f, errF := os.Open(bin)
		if errF != nil {
			errors.Wrap(errF, "Opening file for checksum")
		}
		defer f.Close()

		h := sha256.New()
		if _, errF := io.Copy(h, f); errF != nil {
			errors.Wrap(errF, "Copying file to writer for checksum")
		}

		s := fmt.Sprintf("%x", h.Sum(nil))
		strComp := strings.Compare(s, m.Checksum)
		log.Debugf("|%s| \n|%s| \n Match: %t\n", s, m.Checksum, strComp == 0)
		if strComp != 0 {
			log.Fatal("Checksum not valid")
		}
	}

	dst := filepath.Join(dir, m.Command)
	tmp := dst + ".tmp"

	log.Debugf("copy %q to %q", bin, tmp)
	if err := copyFile(tmp, bin); err != nil {
		return errors.Wrap(err, "copying")
	}

	log.Debugf("renaming %q to %q", bin, dst)
	if err := os.Rename(tmp, dst); err != nil {
		return errors.Wrap(err, "renaming")
	}

	return nil
}

// Install binary to replace the current version.
func (m *Manager) Install(path string) error {
	bin, err := exec.LookPath(m.Command)
	if err != nil {
		return errors.Wrapf(err, "looking up path of %q", m.Command)
	}

	dir := filepath.Dir(bin)
	return m.InstallTo(path, dir)
}

// FindTarball returns a tarball matching os and arch, or nil.
func (r *Release) FindTarball(os, arch string) *Asset {
	s := fmt.Sprintf("%s_%s", os, arch)
	for _, a := range r.Assets {
		ext := filepath.Ext(a.Name)
		if strings.Contains(a.Name, s) && ext == ".gz" {
			return a
		}
	}

	return nil
}

// Download the asset to a tmp directory and return its path.
func (a *Asset) Download() (string, error) {
	return a.DownloadProxy(NopProxy)
}

// DownloadProxy the asset to a tmp directory and return its path.
func (a *Asset) DownloadProxy(proxy Proxy) (string, error) {
	f, err := ioutil.TempFile(os.TempDir(), "update-")
	if err != nil {
		return "", errors.Wrap(err, "creating temp file")
	}

	log.Debugf("fetch %q", a.URL)
	res, err := http.Get(a.URL)
	if err != nil {
		return "", errors.Wrap(err, "fetching asset")
	}

	kind := res.Header.Get("Content-Type")
	size, _ := strconv.Atoi(res.Header.Get("Content-Length"))
	log.Debugf("response %s – %s (%d KiB)", res.Status, kind, size/1024)

	body := proxy(size, res.Body)

	if res.StatusCode >= 400 {
		body.Close()
		return "", errors.Wrap(err, res.Status)
	}

	log.Debugf("copy to %q", f.Name())
	if _, err := io.Copy(f, body); err != nil {
		body.Close()
		return "", errors.Wrap(err, "copying body")
	}

	if err := body.Close(); err != nil {
		return "", errors.Wrap(err, "closing body")
	}

	if err := f.Close(); err != nil {
		return "", errors.Wrap(err, "closing file")
	}

	log.Debugf("copied")
	return f.Name(), nil
}

// copyFile copies the contents of the file named src to the file named
// by dst. The file will be created if it does not already exist. If the
// destination file exists, all it's contents will be replaced by the contents
// of the source file. The file mode will be copied from the source and
// the copied data is synced/flushed to stable storage.
func copyFile(dst, src string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return
	}

	defer func() {
		if e := out.Close(); e != nil {
			err = e
		}
	}()

	_, err = io.Copy(out, in)
	if err != nil {
		return
	}

	err = out.Sync()
	if err != nil {
		return
	}

	si, err := os.Stat(src)
	if err != nil {
		return
	}

	err = os.Chmod(dst, si.Mode())
	if err != nil {
		return
	}

	return
}
