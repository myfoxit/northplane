// np-gen is Northplane's scaffolding generator: developer-velocity tooling
// that stamps the boilerplate for cross-cutting additions to the codebase.
//
// Adding a new monitored-resource type touches a handful of layers in a
// fixed, mechanical way — a Go model struct, a storage `resources` kind,
// a REST CRUD registration, and a frontend type + admin page. np-gen
// stamps house-style, compiling stubs for those seams from a single name
// and prints a precise checklist of the few wiring steps a human must
// still perform (the ones that mean editing hand-maintained registries,
// where a blind rewrite would be more likely to break the build than help).
//
// Usage:
//
//	np-gen new-resource <Name> [flags]
//
// Flags:
//
//	--dry-run        print the plan (files + checklist) without writing
//	--force          overwrite existing files instead of refusing
//	--root <dir>     repository root to write into (default: cwd)
//	--out <dir>      write all generated files flat into <dir> instead of
//	                 their real package directories (handy for trying it
//	                 out without touching the tree, e.g. --out /tmp/np-gen)
package main

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "new-resource":
		if err := newResource(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "np-gen: "+err.Error())
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "np-gen: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `np-gen — Northplane scaffolding generator

Usage:
  np-gen new-resource <Name> [--dry-run] [--force] [--root <dir>] [--out <dir>]

Commands:
  new-resource   Stamp the boilerplate for a new monitored-resource type
                 across the model, storage, API and frontend layers.

Run "np-gen new-resource <Name> --dry-run" to preview the plan.
`)
}

// genFile is one file the generator would produce.
type genFile struct {
	// RelPath is relative to the repo root (or flattened under --out).
	RelPath string
	// Template is the embedded template name.
	Template string
	// What this stub is, for the plan output.
	Desc string
}

// plan returns the files new-resource stamps, in a stable order.
func plan(n Names) []genFile {
	return []genFile{
		{
			RelPath:  filepath.Join("internal", "model", "gen_"+n.Snake+".go"),
			Template: "model.go.tmpl",
			Desc:     "Go model struct (" + n.Pascal + ")",
		},
		{
			RelPath:  filepath.Join("internal", "storage", "gen_"+n.Snake+"_kind.go"),
			Template: "storage_kind.go.tmpl",
			Desc:     "storage resources-kind constant (" + n.ConstName + ` = "` + n.Kebab + `")`,
		},
		{
			RelPath:  filepath.Join("internal", "api", "gen_"+n.Snake+".go"),
			Template: "api.go.tmpl",
			Desc:     "REST CRUD registration (register" + n.PascalPlural + ")",
		},
		{
			RelPath:  filepath.Join("web", "src", "types", "gen_"+n.Kebab+".ts"),
			Template: "types.ts.tmpl",
			Desc:     "frontend type (" + n.Pascal + ")",
		},
		{
			RelPath:  filepath.Join("web", "src", "pages", n.PascalPlural+".tsx"),
			Template: "page.tsx.tmpl",
			Desc:     "frontend admin page (" + n.PascalPlural + "Page)",
		},
	}
}

type options struct {
	dryRun bool
	force  bool
	root   string
	out    string
}

func newResource(args []string) error {
	var (
		opt  options
		name string
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			opt.dryRun = true
		case a == "--force":
			opt.force = true
		case a == "--root":
			i++
			if i >= len(args) {
				return fmt.Errorf("--root needs a value")
			}
			opt.root = args[i]
		case a == "--out":
			i++
			if i >= len(args) {
				return fmt.Errorf("--out needs a value")
			}
			opt.out = args[i]
		case len(a) > 0 && a[0] == '-':
			return fmt.Errorf("unknown flag %q", a)
		default:
			if name != "" {
				return fmt.Errorf("unexpected extra argument %q", a)
			}
			name = a
		}
	}
	if name == "" {
		return fmt.Errorf("new-resource needs a <Name>, e.g. np-gen new-resource Widget")
	}
	if opt.root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		opt.root = cwd
	}

	n := ParseName(name)
	files := plan(n)

	tpls, err := template.ParseFS(templatesFS, "templates/*.tmpl")
	if err != nil {
		return fmt.Errorf("parsing templates: %w", err)
	}

	fmt.Printf("np-gen new-resource %s\n", n.Pascal)
	fmt.Printf("  derived: Pascal=%s plural=%s camel=%s kebab=%s kebabPlural=%s snake=%s kind=%q\n\n",
		n.Pascal, n.PascalPlural, n.Camel, n.Kebab, n.KebabPlural, n.Snake, n.Kebab)

	verb := "Would write"
	if !opt.dryRun {
		verb = "Writing"
	}

	for _, f := range files {
		dest := destPath(opt, f)
		exists := fileExists(dest)
		status := ""
		if exists {
			if opt.force {
				status = " (OVERWRITE --force)"
			} else if !opt.dryRun {
				return fmt.Errorf("refusing to overwrite existing file %s (use --force)", dest)
			} else {
				status = " (EXISTS — would skip without --force)"
			}
		}
		fmt.Printf("  %s  %-52s  %s%s\n", verb, f.RelPath, f.Desc, status)

		if opt.dryRun {
			continue
		}
		if exists && !opt.force {
			continue
		}
		var buf []byte
		buf, err = render(tpls, f.Template, n)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err = os.WriteFile(dest, buf, 0o644); err != nil {
			return err
		}
	}

	fmt.Print("\n" + checklist(n, opt))
	return nil
}

// destPath maps a planned file to its on-disk location. With --out, every
// file is written flat under that directory so the generator never touches
// the live tree while you try it out. Flattened names are prefixed with
// the layer (the relative dir, "_"-joined) so files that share a basename
// across packages — e.g. internal/model/gen_x.go and internal/api/gen_x.go
// — do not collide.
func destPath(opt options, f genFile) string {
	if opt.out != "" {
		dir := filepath.Dir(f.RelPath)
		prefix := strings.ReplaceAll(filepath.ToSlash(dir), "/", "_")
		return filepath.Join(opt.out, prefix+"__"+filepath.Base(f.RelPath))
	}
	return filepath.Join(opt.root, f.RelPath)
}

func render(tpls *template.Template, name string, n Names) ([]byte, error) {
	var buf bytes.Buffer
	if err := tpls.ExecuteTemplate(&buf, name, n); err != nil {
		return nil, fmt.Errorf("rendering %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
