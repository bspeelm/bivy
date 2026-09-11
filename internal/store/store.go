// Package store is the only package that writes outside the project's own
// tree, and PLAN.md §8 is the contract it keeps.
//
// Two directories, ever: one for configuration, one for data. No cache and no
// partial files. The isolation test runs a first launch against a scratch home
// directory and asserts the write set matches that list exactly, because a
// promise nothing checks is a wish.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// The directory bivy owns inside whichever base directory applies.
const dirName = "bivy"

const (
	// Owner only. The follow list is a profile of what someone watches, and
	// while it is not a credential it is nobody else's business (ADR-001).
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// Store reads and writes bivy's state.
type Store struct {
	ConfigDir string
	DataDir   string
}

// Open locates the two directories without creating them: a command that only
// reads should leave nothing behind on a machine where bivy has never run.
func Open() (*Store, error) {
	config, err := baseDir("XDG_CONFIG_HOME", configFallback)
	if err != nil {
		return nil, err
	}
	data, err := baseDir("XDG_DATA_HOME", dataFallback)
	if err != nil {
		return nil, err
	}
	return &Store{ConfigDir: config, DataDir: data}, nil
}

// Dirs is every directory bivy may create, which is the list §8 names and the
// list the isolation test holds it to.
func (s *Store) Dirs() []string {
	if s.ConfigDir == s.DataDir {
		return []string{s.DataDir}
	}
	return []string{s.ConfigDir, s.DataDir}
}

// baseDir applies the XDG variable when it names an absolute path, and the
// platform's own convention otherwise — on macOS too, which is not what Apple
// documents. Someone who has set XDG_DATA_HOME has said where they want
// application data, and ignoring them is how a program ends up somewhere
// surprising.
func baseDir(env string, fallback func(home string) string) (string, error) {
	if d := os.Getenv(env); filepath.IsAbs(d) {
		return filepath.Join(d, dirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory: %w", err)
	}
	return filepath.Join(fallback(home), dirName), nil
}

func configFallback(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support")
	}
	return filepath.Join(home, ".config")
}

func dataFallback(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support")
	}
	return filepath.Join(home, ".local", "share")
}

// ReadJSON loads a data file. A file that is not there is not an error: the
// first launch reads a follow list that does not exist yet, and that is the
// normal case rather than a failure.
func (s *Store) ReadJSON(name string, v any) error {
	b, err := os.ReadFile(filepath.Join(s.DataDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s is not readable as bivy wrote it: %w", name, err)
	}
	return nil
}

// WriteJSON saves a data file atomically: written to a temporary file in the
// same directory and renamed over the target, because a half-written follow
// list is worse than an old one. The temporary file is removed on every path
// out, including the failing ones — a program that promises no residue does
// not get to leave some when it is having a bad day.
func (s *Store) WriteJSON(name string, v any) (err error) {
	if err := os.MkdirAll(s.DataDir, dirPerm); err != nil {
		return err
	}

	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(s.DataDir, "."+name+".*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	if err = tmp.Chmod(filePerm); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	// Durability before visibility. A rename that lands ahead of the bytes it
	// points at survives the process and not the machine.
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.DataDir, name))
}
