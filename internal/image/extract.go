package image

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var ErrZstd = errors.New("camadas zstd não são suportadas")

func Decompress(r io.Reader) (io.Reader, func() error, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	head, _ := br.Peek(4)
	switch {
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, err
		}
		return gz, gz.Close, nil
	case bytes.Equal(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return nil, nil, ErrZstd
	}
	return br, func() error { return nil }, nil
}

func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean("/" + name)
	p := filepath.Join(root, clean)
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("caminho inválido no arquivo: %q", name)
	}
	return p, nil
}

func resolveInside(root, p string) error {
	dir := filepath.Dir(p)
	for d := dir; d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		fi, err := os.Lstat(d)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("o caminho %q atravessa um link simbólico", strings.TrimPrefix(p, root))
		}
	}
	return nil
}

type dirTime struct {
	path string
	t    time.Time
}

func ApplyLayer(root string, r io.Reader) (int64, error) {
	dr, closer, err := Decompress(r)
	if err != nil {
		return 0, err
	}
	defer closer()
	tr := tar.NewReader(dr)
	var total int64
	var dirs []dirTime
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, err
		}
		name := strings.TrimPrefix(filepath.Clean("/"+hdr.Name), "/")
		if name == "" || name == "." {
			continue
		}
		base := filepath.Base(name)
		dir := filepath.Dir(name)
		if base == ".wh..wh..opq" {
			target, err := safeJoin(root, dir)
			if err != nil {
				return total, err
			}
			entries, _ := os.ReadDir(target)
			for _, e := range entries {
				os.RemoveAll(filepath.Join(target, e.Name()))
			}
			continue
		}
		if strings.HasPrefix(base, ".wh.") {
			target, err := safeJoin(root, filepath.Join(dir, strings.TrimPrefix(base, ".wh.")))
			if err != nil {
				return total, err
			}
			os.RemoveAll(target)
			continue
		}
		path, err := safeJoin(root, name)
		if err != nil {
			return total, err
		}
		if err := resolveInside(root, path); err != nil {
			return total, err
		}
		if fi, err := os.Lstat(path); err == nil && !(fi.IsDir() && hdr.Typeflag == tar.TypeDir) {
			os.RemoveAll(path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return total, err
		}
		mode := os.FileMode(hdr.Mode).Perm()
		if hdr.Mode&04000 != 0 {
			mode |= os.ModeSetuid
		}
		if hdr.Mode&02000 != 0 {
			mode |= os.ModeSetgid
		}
		if hdr.Mode&01000 != 0 {
			mode |= os.ModeSticky
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return total, err
			}
			os.Chmod(path, mode)
			dirs = append(dirs, dirTime{path, hdr.ModTime})
		case tar.TypeReg, tar.TypeRegA:
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return total, err
			}
			n, err := io.Copy(f, tr)
			f.Close()
			if err != nil {
				return total, err
			}
			total += n
			os.Chmod(path, mode)
		case tar.TypeSymlink:
			if err := os.Symlink(hdr.Linkname, path); err != nil {
				return total, err
			}
		case tar.TypeLink:
			src, err := safeJoin(root, hdr.Linkname)
			if err != nil {
				return total, err
			}
			if err := os.Link(src, path); err != nil {
				return total, fmt.Errorf("link %s: %w", name, err)
			}
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			var m uint32 = syscall.S_IFIFO
			if hdr.Typeflag == tar.TypeChar {
				m = syscall.S_IFCHR
			} else if hdr.Typeflag == tar.TypeBlock {
				m = syscall.S_IFBLK
			}
			dev := int((hdr.Devmajor << 8) | (hdr.Devminor & 0xff) | ((hdr.Devminor &^ 0xff) << 12))
			_ = syscall.Mknod(path, m|uint32(hdr.Mode&0o7777), dev)
			continue
		default:
			continue
		}
		_ = os.Lchown(path, hdr.Uid, hdr.Gid)
		if hdr.Typeflag != tar.TypeSymlink {
			os.Chmod(path, mode)
			if hdr.Typeflag != tar.TypeDir {
				_ = os.Chtimes(path, hdr.ModTime, hdr.ModTime)
			}
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Chtimes(dirs[i].path, dirs[i].t, dirs[i].t)
	}
	return total, nil
}

func DirSize(root string) int64 {
	var total int64
	filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}
