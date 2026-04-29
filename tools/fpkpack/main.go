package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	root := flag.String("root", ".", "project root")
	out := flag.String("out", "badblocks-victoria.fpk", "output fpk")
	flag.Parse()

	tmp, err := os.CreateTemp("", "app-*.tgz")
	if err != nil {
		log.Fatal(err)
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpName)

	if err := writeAppTGZ(*root, tmpName); err != nil {
		log.Fatal(err)
	}
	if err := writeFPK(*root, tmpName, *out); err != nil {
		log.Fatal(err)
	}
	log.Printf("created %s", *out)
}

func writeAppTGZ(root, out string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return addTree(tw, filepath.Join(root, "app"), "", true)
}

func writeFPK(root, appTGZ, out string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()

	if err := addFileAs(tw, appTGZ, "app.tgz", 0644); err != nil {
		return err
	}
	for _, p := range []string{"cmd", "config"} {
		if err := addTree(tw, filepath.Join(root, p), p, false); err != nil {
			return err
		}
	}
	for _, p := range []string{"ICON.PNG", "ICON_256.PNG", "manifest"} {
		if err := addFileAs(tw, filepath.Join(root, p), p, 0644); err != nil {
			return err
		}
	}
	return nil
}

func addTree(tw *tar.Writer, base, prefix string, appMode bool) error {
	return filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		if rel == "." {
			if prefix == "" {
				return nil
			}
			return addDir(tw, prefix)
		}
		name := filepath.ToSlash(rel)
		if prefix != "" {
			name = filepath.ToSlash(filepath.Join(prefix, rel))
		}
		if d.IsDir() {
			return addDir(tw, name)
		}
		mode := int64(0644)
		if strings.HasPrefix(name, "cmd/") || name == "bin/fn-badblocks-victoria" || strings.HasSuffix(name, "/bin/fn-badblocks-victoria") {
			mode = 0755
		}
		if appMode && name == "bin/fn-badblocks-victoria" {
			mode = 0755
		}
		return addFileAs(tw, path, name, mode)
	})
}

func addDir(tw *tar.Writer, name string) error {
	return tw.WriteHeader(&tar.Header{
		Name:     filepath.ToSlash(name),
		Mode:     0755,
		Typeflag: tar.TypeDir,
		ModTime:  time.Now(),
	})
}

func addFileAs(tw *tar.Writer, src, name string, mode int64) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     filepath.ToSlash(name),
		Mode:     mode,
		Size:     info.Size(),
		Typeflag: tar.TypeReg,
		ModTime:  info.ModTime(),
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}
