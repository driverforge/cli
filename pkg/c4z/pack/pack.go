// Package pack assembles the .c4z archive (a standard DEFLATE zip).
//
// It reproduces the layout of Control4's build_c4z.py: driver.xml is written from an
// in-memory buffer; file items are added; directory items are walked, skipping
// dotfiles and any squishy module sources (which are bundled instead). The
// squishy Main file is replaced by the squished bundle, and the encryption
// target by its encrypted form, wherever they appear — as a file item or
// inside a dir item — never as their plaintext source. Write refuses to
// produce an archive whose declared bundle or encrypted payload has no home.
package pack

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/driverforge/cli/pkg/c4z/manifest"
)

// Plan is everything pack.Write needs to build one .c4z.
type Plan struct {
	C4zPath   string          // output archive path
	SrcDir    string          // working source directory to read file/dir items from
	DriverXML []byte          // transformed driver.xml, written verbatim into the zip
	Items     []manifest.Item // manifest items

	SquishLua  bool
	MainBase   string          // basename of the squishy Main file (the item replaced by the bundle)
	Bundle     []byte          // squished bundle bytes (when SquishLua)
	ModuleBase map[string]bool // squishy module source basenames to exclude from dir walks

	EncryptEnabled    bool   // a script declared encryption="2"
	EncryptTargetBase string // basename of the file to encrypt
	Encrypted         []byte // its encrypted (PKCS#7) bytes, written under <arc>.encrypted
}

// MissingTargetError is returned when a payload the driver declares (the
// squished bundle or the encrypted script) is not covered by any manifest item,
// so the archive would ship without it — for encryption that would mean the
// plaintext source silently standing in for the protected payload.
type MissingTargetError struct {
	Role   string // "squishy Main" or "encryption target"
	Target string // basename the squishy / driver.xml points at
}

func (e *MissingTargetError) Error() string {
	return fmt.Sprintf("%s %q is not covered by any manifest item; the .c4z would ship without it", e.Role, e.Target)
}

// DuplicateEntryError is returned when two manifest items resolve to the same
// archive path. archive/zip would happily write both entries; which one an
// extractor (or the Director) sees is undefined, so the overlap is refused.
type DuplicateEntryError struct {
	Name string // the colliding archive entry
}

func (e *DuplicateEntryError) Error() string {
	return fmt.Sprintf("two manifest items resolve to the same archive entry %q", e.Name)
}

// packState tracks, across one Write, every archive entry written (duplicates
// are refused — see DuplicateEntryError) and whether the substituted payloads
// landed, backing Write's refusal to emit an archive missing a declared one.
type packState struct {
	written        map[string]bool // archive entries already written
	wroteBundle    bool
	wroteEncrypted bool
}

// claim reserves an archive entry name, erroring when an earlier item already
// wrote it.
func (st *packState) claim(name string) error {
	if st.written[name] {
		return &DuplicateEntryError{Name: name}
	}
	st.written[name] = true
	return nil
}

// writeOnce writes a substituted payload (bundle / encrypted), silently
// skipping when the same target was already reached via another item covering
// the same path — that overlap is legitimate for substitutions, unlike plain
// entries.
func (st *packState) writeOnce(zw *zip.Writer, name string, data []byte) error {
	if st.written[name] {
		return nil // same target reached via another item covering this path
	}
	st.written[name] = true
	return writeBytes(zw, name, data)
}

// writeBytes / writeDiskFile write a plain entry, refusing duplicates.
func (st *packState) writeBytes(zw *zip.Writer, name string, data []byte) error {
	if err := st.claim(name); err != nil {
		return err
	}
	return writeBytes(zw, name, data)
}

func (st *packState) writeDiskFile(zw *zip.Writer, name, srcPath string) error {
	if err := st.claim(name); err != nil {
		return err
	}
	return writeDiskFile(zw, name, srcPath)
}

// Write builds the .c4z described by p.
func Write(p Plan) (err error) {
	if err := os.MkdirAll(filepath.Dir(p.C4zPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(p.C4zPath)
	if err != nil {
		return err
	}
	// Don't leave a partial archive behind on failure. Registered before the
	// close defers so it runs after them (deferred calls run last-in-first-out).
	defer func() {
		if err != nil {
			_ = os.Remove(p.C4zPath)
		}
	}()
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	zw := zip.NewWriter(f)
	defer func() {
		if cerr := zw.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	st := &packState{written: map[string]bool{}}

	// driver.xml always comes from the in-memory buffer.
	if err := st.writeBytes(zw, "driver.xml", p.DriverXML); err != nil {
		return err
	}

	for _, it := range p.Items {
		if it.Type != manifest.ItemFile || it.Exclude {
			continue
		}
		base := filepath.Base(it.Name)
		if base == "driver.xml" {
			continue // handled from the buffer above
		}
		arc := cleanArc(it.C4zDir, base)
		handled, err := p.writeSubstituted(zw, st, arc, base)
		if err != nil {
			return err
		}
		if handled {
			continue
		}
		if err := st.writeDiskFile(zw, arc, filepath.Join(p.SrcDir, filepath.FromSlash(it.Name))); err != nil {
			return err
		}
	}

	for _, it := range p.Items {
		if it.Type != manifest.ItemDir || it.Exclude {
			continue
		}
		if err := p.addDir(zw, st, it); err != nil {
			return err
		}
	}

	// A declared payload that never landed means the archive is broken — and,
	// for encryption, that the plaintext source may be standing in for it.
	if p.SquishLua && !st.wroteBundle {
		return &MissingTargetError{Role: "squishy Main", Target: p.MainBase}
	}
	if p.EncryptEnabled && !st.wroteEncrypted {
		return &MissingTargetError{Role: "encryption target", Target: p.EncryptTargetBase}
	}
	return nil
}

// writeSubstituted writes the squished-bundle / encrypted substitution for base
// at arc when base is one of the special targets, reporting whether it handled
// the file. Both the file-item loop and dir walks route through here so a
// protected payload is never written as its plaintext source.
func (p *Plan) writeSubstituted(zw *zip.Writer, st *packState, arc, base string) (bool, error) {
	switch {
	case p.SquishLua && base == p.MainBase:
		st.wroteBundle = true
		if p.EncryptEnabled {
			st.wroteEncrypted = true
			return true, st.writeOnce(zw, arc+".encrypted", p.Encrypted)
		}
		return true, st.writeOnce(zw, arc, p.Bundle)
	case p.EncryptEnabled && base == p.EncryptTargetBase:
		st.wroteEncrypted = true
		return true, st.writeOnce(zw, arc+".encrypted", p.Encrypted)
	}
	return false, nil
}

func (p *Plan) addDir(zw *zip.Writer, st *packState, item manifest.Item) error {
	abs := filepath.Clean(filepath.Join(p.SrcDir, filepath.FromSlash(item.Name)))

	// base is the in-zip directory for the item root; subdirectories append
	// their walk path relative to it. (build_c4z.py derived the suffix with a
	// substring search — rfind on the item name — which flattened any
	// subdirectory whose name ends with the item's, e.g. js/objs under item
	// "js". A bug in the Control4 tool, deliberately not reproduced.)
	base := filepath.ToSlash(item.C4zDir)
	if base == "" {
		base = filepath.ToSlash(item.Name)
	}

	return walkDir(abs, "", item.Recurse, func(rel string, files []string) error {
		c4zDir := base
		if rel != "" {
			c4zDir = path.Join(base, rel)
		}
		for _, fname := range files {
			if p.SquishLua && p.ModuleBase[fname] {
				continue // bundled into the squished output
			}
			arc := path.Clean(c4zDir + "/" + fname)
			if arc == "driver.xml" {
				continue // always written from the in-memory buffer
			}
			handled, err := p.writeSubstituted(zw, st, arc, fname)
			if err != nil {
				return err
			}
			if handled {
				continue
			}
			if err := st.writeDiskFile(zw, arc, filepath.Join(abs, filepath.FromSlash(rel), fname)); err != nil {
				return err
			}
		}
		return nil
	})
}

// walkDir visits root (and, if recurse, its subdirectories) top-down, skipping
// hidden files and directories. visit receives each directory's slash-separated
// path relative to root ("" for root itself) and its file names.
func walkDir(root, rel string, recurse bool, visit func(rel string, files []string) error) error {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	var files, subdirs []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			subdirs = append(subdirs, name)
		} else {
			files = append(files, name)
		}
	}
	if err := visit(rel, files); err != nil {
		return err
	}
	if !recurse {
		return nil
	}
	for _, d := range subdirs {
		if err := walkDir(root, path.Join(rel, d), true, visit); err != nil {
			return err
		}
	}
	return nil
}

func cleanArc(c4zDir, base string) string {
	c4zDir = filepath.ToSlash(c4zDir)
	if c4zDir == "" {
		return base
	}
	return path.Clean(c4zDir + "/" + base)
}

func writeBytes(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return fmt.Errorf("zip entry %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write zip entry %s: %w", name, err)
	}
	return nil
}

func writeDiskFile(zw *zip.Writer, name, srcPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return fmt.Errorf("zip entry %s: %w", name, err)
	}
	if _, err := io.Copy(w, src); err != nil {
		return fmt.Errorf("write zip entry %s: %w", name, err)
	}
	return nil
}
