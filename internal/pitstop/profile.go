package pitstop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const data = "Data"

// envVar is what ExpandEnvironmentStrings expands.
var envVar = regexp.MustCompile(`%[^%]+%`)

// DataFolder resolves a data_dir value as Chrome++ does. It reports false
// where no folder of the install's is meant: for none, which leaves the
// folder to Chrome, and a relative path, which depends on how Chrome started.
func (e *Engine) DataFolder(value string) (string, bool) {
	if value == "none" {
		return "", false
	}
	if value == "" {
		return e.path(data), true
	}
	value = strings.ReplaceAll(value, "%app%", e.path(app))
	value = envVar.ReplaceAllStringFunc(value, func(v string) string {
		if s, ok := os.LookupEnv(strings.Trim(v, "%")); ok {
			return s
		}
		return v
	})
	return filepath.Clean(value), filepath.IsAbs(value)
}

// MovableData reports whether there is something in from to move, and room
// for it in to.
func MovableData(from, to string) bool {
	if inside(from, to) || inside(to, from) {
		return false
	}
	held, err := os.ReadDir(from)
	if err != nil || len(held) == 0 {
		return false
	}
	held, err = os.ReadDir(to)
	return errors.Is(err, fs.ErrNotExist) || err == nil && len(held) == 0
}

func inside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// MoveData moves the user data folder. Chrome must be closed. If it fails,
// from is as it was, unless all of it reached to and part of it is left
// behind.
func (e *Engine) MoveData(ctx context.Context, from, to string) error {
	l, err := e.lock()
	if err != nil {
		return err
	}
	defer l.Unlock()
	if err := e.closed(); err != nil {
		return err
	}
	if !MovableData(from, to) {
		return fmt.Errorf("can't move %s to %s", from, to)
	}
	e.report(Moving)(0, 0)
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	if err := os.Remove(to); err != nil { // a rename wants it gone
		return err
	}
	if os.Rename(from, to) == nil {
		return nil
	}
	return e.moveByCopy(ctx, from, to)
}

// moveByCopy moves a folder across volumes, where a rename can't.
func (e *Engine) moveByCopy(ctx context.Context, from, to string) error {
	partial, err := os.MkdirTemp(filepath.Dir(to), filepath.Base(to)+".partial-*")
	if err != nil {
		return err
	}
	err = copyTree(ctx, from, partial, e.report(Moving))
	if running := e.closed(); running != nil {
		err = running
	}
	if err == nil {
		err = os.Rename(partial, to)
	}
	if err != nil {
		os.RemoveAll(partial)
		return err
	}
	return os.RemoveAll(from)
}

// copyTree copies every file in src to dst, which it creates, or fails.
func copyTree(ctx context.Context, src, dst string, report func(done, total int64)) error {
	var dirs, files []string
	var total int64
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, rel)
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s isn't a file Pitstop can move", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, rel)
		total += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	for _, rel := range dirs {
		if err := os.MkdirAll(filepath.Join(dst, rel), 0o755); err != nil {
			return err
		}
	}
	c := &counter{ctx: ctx, total: total, report: report}
	for _, rel := range files {
		if err := copyFile(filepath.Join(src, rel), filepath.Join(dst, rel), c); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, c *counter) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	c.r = in
	_, err = io.Copy(out, c)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// counter reads a file being copied, reports the bytes copied so far and
// stops once ctx is done.
type counter struct {
	ctx         context.Context
	r           io.Reader
	done, total int64
	report      func(done, total int64)
}

func (c *counter) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(p)
	c.done += int64(n)
	c.report(c.done, c.total)
	return n, err
}
