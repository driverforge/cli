// Package c4z builds a Control4 ".c4z" driver entirely in Go, with no external
// runtime dependencies. It is an independent implementation of the packaging
// format — the same format Control4's Python driverpackager produces by
// shelling out to luajit squish and OpenSSL. See docs/provenance.md for what
// that compatibility does and does not involve.
//
// The package is UI-agnostic: progress is reported through Options.Progress as
// plain Event values, so a CLI can render a TUI while the library stays headless
// and reusable. Build never mutates the source tree (it works in a temp copy);
// the only intentional source write is the optional driver.xml version bump.
package c4z

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/driverforge/cli/pkg/c4z/driverxml"
	"github.com/driverforge/cli/pkg/c4z/encrypt"
	"github.com/driverforge/cli/pkg/c4z/manifest"
	"github.com/driverforge/cli/pkg/c4z/pack"
	"github.com/driverforge/cli/pkg/c4z/sourcemap"
	"github.com/driverforge/cli/pkg/c4z/squish"
)

// Step identifies a stage of the build pipeline (for progress reporting).
type Step string

const (
	StepResolve    Step = "resolve"
	StepVersion    Step = "version"
	StepManifest   Step = "manifest"
	StepPrepackage Step = "prepackage"
	StepPrepare    Step = "prepare"
	StepSquish     Step = "squish"
	StepDriverXML  Step = "driverxml"
	StepEncrypt    Step = "encrypt"
	StepPack       Step = "pack"
	StepUnpack     Step = "unpack"
	StepSourcemap  Step = "sourcemap"
	StepDone       Step = "done"
)

// Status is the outcome reported for a Step.
type Status int

const (
	StepStart Status = iota
	StepOK
	StepSkip
	StepFail
)

// Event is a single progress report. Err is set only with StepFail.
type Event struct {
	Step   Step
	Status Status
	Msg    string
	Err    error
}

// Options configures a build.
type Options struct {
	Configuration string // build configuration, e.g. "release" ("" => default build using config.lua)
	SrcDir        string // driver source directory (contains driver.xml, manifest.c4zproj, squishy)
	DistDir       string // output directory for artifacts

	// Version, when set, is stamped verbatim as the driver's integer <version>
	// (and persisted to the source driver.xml after a successful build). The
	// CLI computes it — from the project's versioning scheme for --increment
	// and deploys, or the user's --version value (driverpackager parity). Empty
	// leaves <version> exactly as authored.
	Version   string
	Unpack    bool // also extract the .c4z to DistDir/<ArtifactName>/
	Sourcemap bool // emit DistDir/<ArtifactName>.lua.map
	Verbose   bool

	// ManifestPath overrides the manifest location ("" => SrcDir/manifest.c4zproj),
	// for driverpackager parity where the .c4zproj name is a first-class input.
	ManifestPath string

	// AllowExecute appends C4:AllowExecute(true) and gIsDevelopmentVersionOfDriver
	// to the driver script (in the work copy only — the source is never touched),
	// enabling Director's Lua command window for development builds. Parity with
	// driverpackager's -ae/--allowexecute.
	AllowExecute bool

	// Encrypt overrides script encryption for this build without touching the
	// source driver.xml: nil uses driver.xml as authored, *true forces
	// encryption on, *false forces it off.
	Encrypt *bool

	// NoSuffix builds a named configuration under the naked driver name: no
	// "-<configuration>" artifact suffix and no " (<configuration>)" display-name
	// suffix. The output then shares the default build's path in DistDir —
	// deliberate for release configurations. No effect on a default build,
	// which is never suffixed.
	NoSuffix bool

	Now       time.Time         // clock for the driver.xml <modified> stamp; zero => time.Now()
	Encryptor encrypt.Encryptor // nil => encrypt.Default()
	Progress  func(Event)       // nil => silent
}

// Result describes the artifacts a successful build produced.
type Result struct {
	Name          string
	ArtifactName  string
	Version       string
	C4zPath       string
	UnpackedDir   string
	SourceMapPath string
	Encrypted     bool
}

// Build runs the full pipeline and returns the produced artifacts.
//
// The pipeline is a fixed sequence of phases, each of which either does its work
// or reports that it had nothing to do. Listing them here rather than inlining
// them keeps the shape of a build readable in one screen, and means a failure
// anywhere reports against the step that owns it without every phase repeating
// that plumbing.
func Build(ctx context.Context, o Options) (Result, error) {
	b := newBuilder(ctx, o)
	defer b.cleanupWork()

	for _, p := range []struct {
		step Step
		run  func() error
	}{
		{StepResolve, b.resolve},
		{StepManifest, b.checkManifest},
		{StepVersion, b.stampVersion},
		{StepPrepackage, b.runPrepackage},
		{StepPrepare, b.prepareWorkTree},
		{StepSquish, b.squishLua},
		{StepDriverXML, b.transformDriverXML},
		{StepEncrypt, b.encryptScript},
		{StepPack, b.packArtifact},
		{StepUnpack, b.unpackArtifact},
		{StepSourcemap, b.writeSourceMap},
		{StepVersion, b.persistVersion},
	} {
		if err := p.run(); err != nil {
			b.emit(p.step, StepFail, "", err)
			return Result{}, err
		}
	}

	// Postpackage runs after everything the progress UI narrates, and a failure
	// here is returned without a step event — as it always has been, because
	// there is no step still open to attribute it to.
	if len(b.man.Post) > 0 {
		if err := runCommands(ctx, b.man.Post, o.SrcDir); err != nil {
			return Result{}, err
		}
	}

	b.emit(StepDone, StepOK, "", nil)
	return b.res, nil
}

// builder is the state one Build threads through its phases. It exists so each
// phase can be a method with a single job rather than a section of one long
// function; the fields are grouped in the order the pipeline fills them.
type builder struct {
	ctx context.Context
	o   Options
	enc encrypt.Encryptor
	now time.Time

	// resolve
	driverXMLPath string
	srcDriverXML  []byte
	man           *manifest.Driver
	name          string

	// versioning: computed before the build, written back only once the artifact
	// exists, so a failed build never leaves the source tree mutated
	version          string
	versionWriteBack []byte

	// naming
	artifact string
	suffix   string

	// working copy
	work    string
	cleanup func()

	// squish
	sq         squish.Squishy
	bundle     []byte
	mainBase   string
	moduleBase map[string]bool

	// driver.xml + encryption
	outXML        []byte
	encInfo       driverxml.EncryptionInfo
	encrypted     []byte
	encTargetBase string

	// outputs
	c4zPath     string
	unpackedDir string
	mapPath     string

	res Result
}

func newBuilder(ctx context.Context, o Options) *builder {
	b := &builder{ctx: ctx, o: o, enc: o.Encryptor, now: o.Now, moduleBase: map[string]bool{}}
	if b.enc == nil {
		b.enc = encrypt.Default()
	}
	if b.now.IsZero() {
		b.now = time.Now()
	}
	return b
}

func (b *builder) emit(step Step, status Status, msg string, err error) {
	if b.o.Progress != nil {
		b.o.Progress(Event{Step: step, Status: status, Msg: msg, Err: err})
	}
}

func (b *builder) cleanupWork() {
	if b.cleanup != nil {
		b.cleanup()
	}
}

// resolve reads the inputs a build is defined by: the driver.xml to transform and
// the manifest describing what goes into the archive. A named configuration must
// exist before anything else happens, so a typo fails before any work.
func (b *builder) resolve() error {
	b.emit(StepResolve, StepStart, "", nil)
	b.driverXMLPath = filepath.Join(b.o.SrcDir, "driver.xml")
	src, err := os.ReadFile(b.driverXMLPath)
	if err != nil {
		return fmt.Errorf("read driver.xml: %w", err)
	}
	b.srcDriverXML = src

	manifestPath := b.o.ManifestPath
	if manifestPath == "" {
		manifestPath = filepath.Join(b.o.SrcDir, "manifest.c4zproj")
	}
	man, err := manifest.Parse(manifestPath)
	if err != nil {
		return err
	}
	b.man = man
	b.name = man.Name

	// A configuration is optional: when set it swaps the config.<configuration>.lua
	// override in for config.lua and applies the name suffix. A default build (no
	// configuration) uses the committed config.lua as-is.
	if b.o.Configuration != "" {
		if _, err := os.Stat(filepath.Join(b.o.SrcDir, "config."+b.o.Configuration+".lua")); err != nil {
			return &ConfigNotFoundError{Configuration: b.o.Configuration, Dir: b.o.SrcDir}
		}
	}
	b.emit(StepResolve, StepOK, b.name, nil)
	return nil
}

// checkManifest validates before versioning, because the version bump is the
// build's only source-tree mutation and nothing may fail-fast after it.
func (b *builder) checkManifest() error {
	b.emit(StepManifest, StepStart, "", nil)
	if b.man.Type != "c4z" {
		return &UnsupportedDriverTypeError{Type: b.man.Type}
	}
	b.emit(StepManifest, StepOK, "", nil)
	return nil
}

// stampVersion computes the stamped driver.xml. It is persisted only after the
// build succeeds (see persistVersion), so a failed build leaves the source alone.
func (b *builder) stampVersion() error {
	if b.o.Version == "" {
		// Leave <version> exactly as authored.
		b.version, _ = driverxml.ReadVersion(b.srcDriverXML)
		b.emit(StepVersion, StepSkip, "as authored", nil)
		b.setNaming()
		return nil
	}

	// Stamped verbatim: the CLI computed this from the project's versioning scheme
	// (--increment, deploy) or the user supplied it (--version, driverpackager
	// --driver-version parity).
	b.emit(StepVersion, StepStart, b.o.Version, nil)
	v := strings.TrimSpace(b.o.Version)
	bumped, err := driverxml.WriteVersion(b.srcDriverXML, v)
	if err != nil {
		return err
	}
	b.versionWriteBack = bumped
	b.version = v
	b.emit(StepVersion, StepOK, v, nil)
	b.setNaming()
	return nil
}

// setNaming derives the configuration-specific artifact name and display suffix,
// and seeds the result with what is already known.
func (b *builder) setNaming() {
	b.artifact = artifactName(b.name, b.o.Configuration, b.o.NoSuffix)
	b.suffix = displayNameSuffix(b.o.Configuration, b.o.NoSuffix)
	b.res.Name, b.res.ArtifactName, b.res.Version = b.name, b.artifact, b.version
}

func (b *builder) runPrepackage() error {
	if len(b.man.Pre) == 0 {
		return nil
	}
	b.emit(StepPrepackage, StepStart, "", nil)
	if err := runCommands(b.ctx, b.man.Pre, b.o.SrcDir); err != nil {
		return err
	}
	b.emit(StepPrepackage, StepOK, "", nil)
	return nil
}

// prepareWorkTree copies the source into a temp directory and applies everything
// that must not touch the real tree: the configuration's config swap, the bumped
// driver.xml, and (for a development build) the AllowExecute append.
func (b *builder) prepareWorkTree() error {
	b.emit(StepPrepare, StepStart, "", nil)
	work, cleanup, err := makeWorkDir(b.o.SrcDir, b.o.Configuration)
	if err != nil {
		return err
	}
	b.work, b.cleanup = work, cleanup

	// Seed the work copy with the bumped driver.xml (the source itself is only
	// written after a successful build), so the rest of the pipeline sees the same
	// document a persisted bump would have produced.
	if b.versionWriteBack != nil {
		if err := os.WriteFile(filepath.Join(b.work, "driver.xml"), b.versionWriteBack, 0o644); err != nil {
			return err
		}
	}

	msg := ""
	if b.o.AllowExecute {
		if err := b.appendAllowExecute(); err != nil {
			return err
		}
		msg = "C4:AllowExecute enabled (development build)"
	}
	b.emit(StepPrepare, StepOK, msg, nil)
	return nil
}

// appendAllowExecute enables Director's Lua command window by appending to the
// driver script in the WORK copy — squish concatenates the main file last, so the
// lines land at the bundle's tail; a non-squished script is packaged with them in
// place. The source is never touched (driverpackager parity, minus its .tmp
// backup dance).
func (b *builder) appendAllowExecute() error {
	scriptRel, ok := driverxml.ScriptFile(b.srcDriverXML)
	if !ok {
		return &NoScriptFileError{Flag: "--allow-execute"}
	}
	f, err := os.OpenFile(filepath.Join(b.work, filepath.FromSlash(scriptRel)), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.WriteString("\nC4:AllowExecute(true)\ngIsDevelopmentVersionOfDriver = true\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func (b *builder) squishLua() error {
	if !b.man.SquishLua {
		b.emit(StepSquish, StepSkip, "", nil)
		return nil
	}
	b.emit(StepSquish, StepStart, "", nil)
	sq, err := squish.ParseFile(filepath.Join(b.work, "squishy"))
	if err != nil {
		return err
	}
	bundle, err := squish.Bundle(b.work, sq)
	if err != nil {
		return err
	}
	b.sq, b.bundle = sq, bundle
	b.mainBase = filepath.Base(filepath.FromSlash(sq.Main))
	for _, m := range sq.Modules {
		b.moduleBase[filepath.Base(filepath.FromSlash(m.Path))] = true
	}
	b.emit(StepSquish, StepOK, fmt.Sprintf("%d modules", len(sq.Modules)), nil)
	return nil
}

func (b *builder) transformDriverXML() error {
	b.emit(StepDriverXML, StepStart, "", nil)
	srcXML, err := os.ReadFile(filepath.Join(b.work, "driver.xml"))
	if err != nil {
		return err
	}
	outXML, encInfo, err := driverxml.Transform(srcXML, driverxml.Edits{
		NameSuffix: b.suffix, Modified: b.now, Encrypt: b.o.Encrypt,
	})
	if err != nil {
		return err
	}
	b.outXML, b.encInfo = outXML, encInfo
	b.emit(StepDriverXML, StepOK, "", nil)
	return nil
}

func (b *builder) encryptScript() error {
	if !b.encInfo.Enabled {
		// --encrypt was explicit; a silent plaintext build would betray it.
		if b.o.Encrypt != nil && *b.o.Encrypt {
			return &NoEncryptableScriptError{}
		}
		b.emit(StepEncrypt, StepSkip, "", nil)
		return nil
	}

	b.emit(StepEncrypt, StepStart, b.encInfo.File, nil)
	plain := b.bundle // the squished output is what gets encrypted
	if !b.man.SquishLua {
		var err error
		plain, err = os.ReadFile(filepath.Join(b.work, filepath.FromSlash(b.encInfo.File)))
		if err != nil {
			return err
		}
	}
	encrypted, err := b.enc.Encrypt(plain)
	if err != nil {
		return err
	}
	b.encrypted = encrypted
	b.encTargetBase = filepath.Base(filepath.FromSlash(b.encInfo.File))
	b.res.Encrypted = true
	b.emit(StepEncrypt, StepOK, "", nil)
	return nil
}

// packArtifact clears this configuration's previous outputs, then writes the .c4z.
func (b *builder) packArtifact() error {
	b.emit(StepPack, StepStart, "", nil)
	b.c4zPath = filepath.Join(b.o.DistDir, b.artifact+".c4z")
	b.unpackedDir = filepath.Join(b.o.DistDir, b.artifact)
	b.mapPath = filepath.Join(b.o.DistDir, b.artifact+".lua.map")
	_ = os.Remove(b.c4zPath)
	_ = os.RemoveAll(b.unpackedDir)
	_ = os.Remove(b.mapPath)

	if err := pack.Write(pack.Plan{
		C4zPath: b.c4zPath, SrcDir: b.work, DriverXML: b.outXML, Items: b.man.Items,
		SquishLua: b.man.SquishLua, MainBase: b.mainBase, Bundle: b.bundle, ModuleBase: b.moduleBase,
		EncryptEnabled: b.encInfo.Enabled, EncryptTargetBase: b.encTargetBase, Encrypted: b.encrypted,
	}); err != nil {
		return err
	}
	b.res.C4zPath = b.c4zPath
	b.emit(StepPack, StepOK, b.c4zPath, nil)
	return nil
}

func (b *builder) unpackArtifact() error {
	if !b.o.Unpack {
		return nil
	}
	b.emit(StepUnpack, StepStart, "", nil)
	if err := unzip(b.c4zPath, b.unpackedDir); err != nil {
		return err
	}
	b.res.UnpackedDir = b.unpackedDir
	b.emit(StepUnpack, StepOK, b.unpackedDir, nil)
	return nil
}

func (b *builder) writeSourceMap() error {
	if !b.o.Sourcemap {
		return nil
	}
	if !b.man.SquishLua {
		b.emit(StepSourcemap, StepSkip, "driver is not squished", nil)
		return nil
	}
	b.emit(StepSourcemap, StepStart, "", nil)
	outBase := filepath.Base(filepath.FromSlash(b.sq.Output))
	sm, err := sourcemap.Generate(b.sq, b.work, b.bundle, outBase, b.now)
	if err != nil {
		return err
	}
	data, err := sm.JSON()
	if err != nil {
		return err
	}
	if err := os.WriteFile(b.mapPath, data, 0o644); err != nil {
		return err
	}
	b.res.SourceMapPath = b.mapPath
	b.emit(StepSourcemap, StepOK, b.mapPath, nil)
	return nil
}

// persistVersion records the bump in the source tree. The artifact is fully
// produced by now, and this runs before the postpackage commands, which may read
// the bumped driver.xml.
func (b *builder) persistVersion() error {
	if b.versionWriteBack == nil {
		return nil
	}
	return os.WriteFile(b.driverXMLPath, b.versionWriteBack, 0o644)
}

// makeWorkDir copies the source tree to a temp directory and, when a configuration
// is given, swaps the config.<configuration>.lua override in as config.lua, so the
// real source tree is never mutated. With no configuration (a default build) the
// committed config.lua is used as-is.
func makeWorkDir(srcDir, configuration string) (string, func(), error) {
	work, err := os.MkdirTemp("", "driverforge-build-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(work) }
	if err := copyTree(srcDir, work); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if configuration == "" {
		return work, cleanup, nil
	}
	override, err := os.ReadFile(filepath.Join(srcDir, "config."+configuration+".lua"))
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := os.WriteFile(filepath.Join(work, "config.lua"), override, 0o644); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return work, cleanup, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks/devices
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func unzip(c4zPath, dest string) error {
	r, err := zip.OpenReader(c4zPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, zf := range r.File {
		target := filepath.Join(dest, zf.Name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("unzip: unsafe path %q", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// runCommands executes manifest Pre/PostpackageCommands via the shell, in dir.
// This is the only place the build shells out, and it runs the manifest
// author's own declared commands (for driverpackager parity), not a tool
// dependency of ours.
func runCommands(ctx context.Context, cmds []string, dir string) error {
	for _, c := range cmds {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", c)
		cmd.Dir = dir
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("package command failed (%q): %w", c, err)
		}
	}
	return nil
}
