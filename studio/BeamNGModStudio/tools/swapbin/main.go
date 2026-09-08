// Command swapbin installs a freshly built executable over the one the app
// normally runs, even while that executable is running.
//
// Windows locks a running image against deletion and overwriting, but it does
// allow the file to be renamed. So the swap is: move the live binary aside into
// a "superseded" folder, then move the staged build into its place. A running
// instance keeps executing from the moved file and the next launch picks up the
// new build, so a rebuild never fails just because the app is open.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const supersededDirName = "superseded"

func main() {
	keep := flag.Int("keep", 3, "number of superseded binaries to retain")
	remove := flag.Bool("remove", false, "delete the given files instead of swapping (best effort)")
	flag.Parse()
	if *remove {
		for _, name := range flag.Args() {
			_ = os.Remove(name)
		}
		return
	}
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: swapbin [-keep N] <staged-binary> <target-binary>")
		fmt.Fprintln(os.Stderr, "       swapbin -remove <file>...")
		os.Exit(2)
	}
	if err := swap(flag.Arg(0), flag.Arg(1), *keep); err != nil {
		fmt.Fprintln(os.Stderr, "swapbin:", err)
		os.Exit(1)
	}
}
func swap(staged, target string, keep int) error {
	info, err := os.Stat(staged)
	if err != nil {
		return fmt.Errorf("staged binary: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("staged binary %s is a directory", staged)
	}

	if _, err := os.Stat(target); err == nil {
		if err := archive(target, keep); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect target: %w", err)
	}

	if err := os.Rename(staged, target); err != nil {
		return fmt.Errorf("install %s: %w", target, err)
	}
	fmt.Printf("installed %s\n", target)
	return nil
}

// archive moves the live binary out of the way. Renaming succeeds while the
// image is running; deleting or overwriting it would not.
func archive(target string, keep int) error {
	directory := filepath.Join(filepath.Dir(target), supersededDirName)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", directory, err)
	}
	base := filepath.Base(target)
	extension := filepath.Ext(base)
	name := fmt.Sprintf("%s-%s%s", strings.TrimSuffix(base, extension), time.Now().Format("20060102-150405"), extension)
	moved := filepath.Join(directory, name)
	if err := os.Rename(target, moved); err != nil {
		return fmt.Errorf("move the running binary aside (close the app if this persists): %w", err)
	}
	fmt.Printf("moved previous build to %s\n", moved)
	return prune(directory, extension, keep)
}

// prune keeps the newest retained copies and deletes the rest. A copy still
// locked by a running process simply survives until the next build.
func prune(directory, extension string, keep int) error {
	if keep < 0 {
		return nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), extension) {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= keep {
		return nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names[keep:] {
		_ = os.Remove(filepath.Join(directory, name))
	}
	return nil
}
