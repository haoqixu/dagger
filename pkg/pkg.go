package pkg

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
	"github.com/rs/zerolog/log"
)

var (
	// FS contains the filesystem of the stdlib.
	//go:embed alpha.dagger.io dagger.io universe.dagger.io
	FS embed.FS
)

var (
	AlphaModule    = "alpha.dagger.io"
	DaggerModule   = "dagger.io"
	UniverseModule = "universe.dagger.io"

	modules = []string{
		AlphaModule,
		DaggerModule,
		UniverseModule,
	}

	EnginePackage = fmt.Sprintf("%s/dagger/engine", DaggerModule)

	lockFilePath = "dagger.lock"
)

func Vendor(ctx context.Context, p string) error {
	if p == "" {
		p = GetCueModParent()
	}

	cuePkgDir := path.Join(p, "cue.mod", "pkg")
	if err := os.MkdirAll(cuePkgDir, 0755); err != nil {
		return err
	}

	// Lock this function so no more than 1 process can run it at once.
	lockFile := path.Join(cuePkgDir, lockFilePath)
	l := flock.New(lockFile)
	if err := l.Lock(); err != nil {
		return err
	}
	defer func() {
		l.Unlock()
		os.Remove(lockFile)
	}()

	// ensure cue module is initialized
	if err := cueModInit(ctx, p); err != nil {
		return err
	}

	// generate `.gitignore`
	if err := os.WriteFile(
		path.Join(cuePkgDir, ".gitignore"),
		[]byte(fmt.Sprintf("# generated by dagger\ndagger.lock\n%s", strings.Join(modules, "\n"))),
		0600,
	); err != nil {
		return err
	}

	log.Ctx(ctx).Debug().Str("mod", p).Msg("vendoring packages")

	// Unpack modules in a temporary directory
	unpackDir, err := os.MkdirTemp(cuePkgDir, "vendor-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(unpackDir)

	if err := extractModules(unpackDir); err != nil {
		return err
	}

	for _, module := range modules {
		// Semi-atomic swap of the module
		//
		// The following basically does:
		// $ rm -rf cue.mod/pkg/MODULE.old
		// $ mv cue.mod/pkg/MODULE cue.mod/pkg/MODULE.old
		// $ mv VENDOR/MODULE cue.mod/pkg/MODULE
		// $ rm -rf cue.mod/pkg/MODULE.old

		moduleDir := path.Join(cuePkgDir, module)
		backupModuleDir := moduleDir + ".old"
		if err := os.RemoveAll(backupModuleDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(moduleDir, backupModuleDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		defer os.RemoveAll(backupModuleDir)

		if err := os.Rename(path.Join(unpackDir, module), moduleDir); err != nil {
			return err
		}
	}

	return nil
}

func extractModules(dest string) error {
	return fs.WalkDir(FS, ".", func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.Type().IsRegular() {
			return nil
		}

		contents, err := fs.ReadFile(FS, p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		overlayPath := path.Join(dest, p)

		if err := os.MkdirAll(filepath.Dir(overlayPath), 0755); err != nil {
			return err
		}

		// Give exec permission on embedded file to freely use shell script
		// Exclude permission linter
		//nolint
		return os.WriteFile(overlayPath, contents, 0700)
	})
}

// GetCueModParent traverses the directory tree up through ancestors looking for a cue.mod folder
func GetCueModParent() string {
	cwd, _ := os.Getwd()
	parentDir := cwd

	for {
		if _, err := os.Stat(path.Join(parentDir, "cue.mod")); !errors.Is(err, os.ErrNotExist) {
			break // found it!
		}

		parentDir = filepath.Dir(parentDir)

		if parentDir == string(os.PathSeparator) {
			// reached the root
			parentDir = cwd // reset to working directory
			break
		}
	}

	return parentDir
}

func cueModInit(ctx context.Context, parentDir string) error {
	lg := log.Ctx(ctx)

	modDir := path.Join(parentDir, "cue.mod")
	modFile := path.Join(modDir, "module.cue")
	if _, err := os.Stat(modFile); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}

		lg.Debug().Str("mod", parentDir).Msg("initializing cue.mod")

		if err := os.WriteFile(modFile, []byte("module: \"\"\n"), 0600); err != nil {
			return err
		}
	}

	if err := os.Mkdir(path.Join(modDir, "usr"), 0755); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	if err := os.Mkdir(path.Join(modDir, "pkg"), 0755); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}

	return nil
}
