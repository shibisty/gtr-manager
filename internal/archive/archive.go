package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Extract(src, dst string) error {

	if strings.HasSuffix(src, ".zip") {
		return unzip(src, dst)
	}

	if strings.HasSuffix(src, ".tar.gz") {
		return untargz(src, dst)
	}

	return os.ErrInvalid
}

func unzip(src, dst string) error {

	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {

		path := filepath.Join(dst, f.Name)

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}

		in, err := f.Open()
		if err != nil {
			return err
		}

		out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			in.Close()
			return err
		}

		_, err = io.Copy(out, in)

		in.Close()
		out.Close()

		if err != nil {
			return err
		}
	}

	return nil
}

func untargz(src, dst string) error {

	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	for {

		hdr, err := tr.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		path := filepath.Join(dst, hdr.Name)

		switch hdr.Typeflag {

		case tar.TypeDir:

			if err := os.MkdirAll(path, os.FileMode(hdr.Mode)); err != nil {
				return err
			}

		case tar.TypeReg:

			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}

			out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}

			_, err = io.Copy(out, tr)
			out.Close()

			if err != nil {
				return err
			}
		}
	}

	return nil
}
