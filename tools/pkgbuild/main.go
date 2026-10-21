// Command pkgbuild builds olcWRT packages without the OpenWrt SDK.
//
// It cross-compiles olcrtc and the olcwrt helper with the Go toolchain for
// the requested OpenWrt architectures and packs .ipk files (OpenWrt 23.05 /
// 24.10). With -apk it also creates .apk files (OpenWrt 25.12+) using an
// apk-tools v3 binary, for example apk.static from Alpine.
//
//	cd tools/pkgbuild && go run . -arch mipsel_24kc,aarch64_cortex-a53
//	cd tools/pkgbuild && go run . pot
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type options struct {
	root      string
	olcrtcSrc string
	out       string
	archs     string
	goBin     string
	apk       string
	upx       bool
	noOlcrtc  bool
	olcrtcVer string
}

func main() {
	var o options
	flag.StringVar(&o.root, "root", "", "olcWRT repository root (default: detected)")
	flag.StringVar(&o.olcrtcSrc, "olcrtc", "", "olcrtc source tree (default: <root>/SOURCES/olcrtc)")
	flag.StringVar(&o.out, "out", "", "output directory (default: <root>/dist)")
	flag.StringVar(&o.archs, "arch", "common", "comma separated OpenWrt architectures, \"common\" or \"all\"")
	flag.StringVar(&o.goBin, "go", "go", "Go toolchain binary")
	flag.StringVar(&o.apk, "apk", "", "apk-tools v3 binary to also build .apk packages (\"wsl:/path/apk.static\" runs it in WSL)")
	flag.BoolVar(&o.upx, "upx", false, "compress binaries with upx (must be in PATH)")
	flag.BoolVar(&o.noOlcrtc, "no-olcrtc", false, "do not build the olcrtc package")
	flag.StringVar(&o.olcrtcVer, "olcrtc-version", "", "override the olcrtc package version")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: pkgbuild [flags] [build|pot|archs]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if err := run(&o, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "pkgbuild:", err)
		os.Exit(1)
	}
}

func run(o *options, cmd string) error {
	if o.root == "" {
		root, err := findRoot()
		if err != nil {
			return err
		}
		o.root = root
	}
	switch cmd {
	case "", "build":
		return build(o)
	case "pot":
		return cmdPot(o.root)
	case "archs":
		archs, _ := resolveArchs("all")
		fmt.Println(strings.Join(archs, "\n"))
		return nil
	case "lmo":
		// pkgbuild lmo in.po out.lmo - same as LuCI's po2lmo.
		if flag.NArg() != 3 {
			return fmt.Errorf("usage: pkgbuild lmo in.po out.lmo")
		}
		f, err := os.Open(flag.Arg(1))
		if err != nil {
			return err
		}
		defer f.Close()
		return os.WriteFile(flag.Arg(2), compileLMO(parsePO(f)), 0o644)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "feed", "olcwrt", "Makefile")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot find the olcWRT repository root, use -root")
		}
		dir = parent
	}
}

// makeVar reads NAME:=value from an OpenWrt Makefile.
func makeVar(path, name string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\s*:?=\s*(.*?)\s*$`)
	if m := re.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

type versions struct {
	olcwrt, luci, olcrtc string
	olcwrtRaw            string
}

func readVersions(o *options) versions {
	feed := filepath.Join(o.root, "feed")
	pv := func(pkg string) (string, string) {
		mk := filepath.Join(feed, pkg, "Makefile")
		return makeVar(mk, "PKG_VERSION"), makeVar(mk, "PKG_RELEASE")
	}
	v := versions{}
	ver, rel := pv("olcwrt")
	v.olcwrtRaw = ver
	v.olcwrt = ver + "-r" + rel
	ver, rel = pv("luci-app-olcwrt")
	v.luci = ver + "-r" + rel
	mk := filepath.Join(feed, "olcrtc", "Makefile")
	date := strings.ReplaceAll(makeVar(mk, "PKG_SOURCE_DATE"), "-", ".")
	commit := makeVar(mk, "PKG_SOURCE_VERSION")
	if len(commit) > 8 {
		commit = commit[:8]
	}
	v.olcrtc = date + "~" + commit + "-r" + makeVar(mk, "PKG_RELEASE")
	if o.olcrtcVer != "" {
		v.olcrtc = o.olcrtcVer
	}
	return v
}

func build(o *options) error {
	if o.olcrtcSrc == "" {
		o.olcrtcSrc = filepath.Join(o.root, "SOURCES", "olcrtc")
	}
	if o.out == "" {
		o.out = filepath.Join(o.root, "dist")
	}
	archs, err := resolveArchs(o.archs)
	if err != nil {
		return err
	}
	if !o.noOlcrtc {
		if _, err := os.Stat(filepath.Join(o.olcrtcSrc, "cmd", "olcrtc")); err != nil {
			return fmt.Errorf("olcrtc sources not found in %s (use -olcrtc or -no-olcrtc)", o.olcrtcSrc)
		}
	}
	v := readVersions(o)
	mtime := time.Now().UTC().Truncate(time.Second)
	binDir := filepath.Join(o.out, "bin")

	// Build every distinct Go target once.
	built := map[string]bool{}
	for _, a := range archs {
		t := archTable[a]
		if built[t.key()] {
			continue
		}
		built[t.key()] = true
		dir := filepath.Join(binDir, t.key())
		helper := filepath.Join(o.root, "feed", "olcwrt", "src")
		fmt.Printf("==> olcwrt %s (%s)\n", v.olcwrt, t.key())
		if err := goBuild(o, helper, ".", filepath.Join(dir, "olcwrt"), t, "-s -w -X main.version="+v.olcwrtRaw); err != nil {
			return err
		}
		if !o.noOlcrtc {
			fmt.Printf("==> olcrtc %s (%s)\n", v.olcrtc, t.key())
			if err := goBuild(o, o.olcrtcSrc, "./cmd/olcrtc", filepath.Join(dir, "olcrtc"), t, "-s -w"); err != nil {
				return err
			}
		}
	}

	var specs []*pkgSpec
	for _, a := range archs {
		dir := filepath.Join(binDir, archTable[a].key())
		if !o.noOlcrtc {
			s, err := olcrtcSpec(v, a, filepath.Join(dir, "olcrtc"))
			if err != nil {
				return err
			}
			specs = append(specs, s)
		}
		s, err := olcwrtSpec(o, v, a, filepath.Join(dir, "olcwrt"))
		if err != nil {
			return err
		}
		specs = append(specs, s)
	}
	luci, err := luciSpec(o, v)
	if err != nil {
		return err
	}
	specs = append(specs, luci)
	i18n, err := i18nSpecs(o, v)
	if err != nil {
		return err
	}
	specs = append(specs, i18n...)

	for _, s := range specs {
		out, err := writeIPK(s, filepath.Join(o.out, "ipk", s.Arch), mtime)
		if err != nil {
			return err
		}
		fmt.Println("ipk:", rel(o.out, out))
	}
	if o.apk != "" {
		if err := buildAPKs(o, specs, mtime); err != nil {
			return err
		}
	}
	return nil
}

func rel(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil {
		return r
	}
	return p
}

func goBuild(o *options, dir, pkg, out string, t goTarget, ldflags string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	cmd := exec.Command(o.goBin, "build", "-trimpath", "-ldflags", ldflags, "-o", abs, pkg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+t.GOARCH, "CGO_ENABLED=0", "GOWORK=off")
	cmd.Env = append(cmd.Env, t.Env...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build %s for %s: %w", pkg, t.key(), err)
	}
	if o.upx {
		upx := exec.Command("upx", "-q", "--best", "--lzma", abs)
		upx.Stdout, upx.Stderr = os.Stdout, os.Stderr
		if err := upx.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: upx failed for %s: %v\n", abs, err)
		}
	}
	return nil
}

const (
	maintainer = "olcWRT contributors"
	projectURL = "https://github.com/openlibrecommunity/olcrtc"
)

func olcrtcSpec(v versions, arch, bin string) (*pkgSpec, error) {
	data, err := os.ReadFile(bin)
	if err != nil {
		return nil, err
	}
	return &pkgSpec{
		Name: "olcrtc", Version: v.olcrtc, Arch: arch, Depends: []string{"ca-bundle"},
		Section: "net", License: "WTFPL", URL: projectURL, Maintainer: maintainer,
		Description: "olcRTC encrypted TCP-over-WebRTC tunnel (Jitsi, Yandex Telemost, WB Stream).",
		Files:       []pkgFile{{"/usr/bin/olcrtc", 0o755, data}},
	}, nil
}

func olcwrtSpec(o *options, v versions, arch, bin string) (*pkgSpec, error) {
	data, err := os.ReadFile(bin)
	if err != nil {
		return nil, err
	}
	files := []pkgFile{{"/usr/bin/olcwrt", 0o755, data}}
	feedFiles := filepath.Join(o.root, "feed", "olcwrt", "files")
	for _, f := range []struct {
		src, dst string
		mode     int64
	}{
		{"olcwrt.init", "/etc/init.d/olcwrt", 0o755},
		{"olcwrt.config", "/etc/config/olcwrt", 0o600},
		{"proxy.lst", "/etc/olcwrt/proxy.lst", 0o644},
		{"bypass.lst", "/etc/olcwrt/bypass.lst", 0o644},
		{"olcwrt.keep", "/lib/upgrade/keep.d/olcwrt", 0o644},
	} {
		b, err := readText(filepath.Join(feedFiles, f.src))
		if err != nil {
			return nil, err
		}
		files = append(files, pkgFile{f.dst, f.mode, b})
	}
	return &pkgSpec{
		Name: "olcwrt", Version: v.olcwrt, Arch: arch,
		Depends: []string{"olcrtc", "firewall4", "kmod-nft-nat", "ca-bundle"},
		Section: "net", License: "WTFPL", URL: projectURL, Maintainer: maintainer,
		Description: "olcRTC client for OpenWrt: transparent proxy, DNS through the tunnel, lists, subscriptions and failover.",
		Files:       files,
		Conffiles:   []string{"/etc/config/olcwrt", "/etc/olcwrt/proxy.lst", "/etc/olcwrt/bypass.lst"},
		PrermPkg:    "#!/bin/sh\n[ -n \"${IPKG_INSTROOT}\" ] && exit 0\n/etc/init.d/olcwrt stop\n/usr/bin/olcwrt cleanup\nexit 0\n",
	}, nil
}

// readText reads a text file and normalises Windows line endings, which a
// git checkout on Windows may introduce.
func readText(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return []byte(strings.ReplaceAll(string(b), "\r\n", "\n")), nil
}

func luciSpec(o *options, v versions) (*pkgSpec, error) {
	base := filepath.Join(o.root, "feed", "luci-app-olcwrt")
	var files []pkgFile
	add := func(srcRoot, dstRoot string) error {
		return filepath.Walk(srcRoot, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			r, _ := filepath.Rel(srcRoot, p)
			dst := dstRoot + "/" + filepath.ToSlash(r)
			mode := int64(0o644)
			if strings.HasPrefix(dst, "/usr/libexec/") {
				mode = 0o755
			}
			b, err := readText(p)
			if err != nil {
				return err
			}
			files = append(files, pkgFile{dst, mode, b})
			return nil
		})
	}
	if err := add(filepath.Join(base, "htdocs"), "/www"); err != nil {
		return nil, err
	}
	if err := add(filepath.Join(base, "root"), ""); err != nil {
		return nil, err
	}
	return &pkgSpec{
		Name: "luci-app-olcwrt", Version: v.luci, Arch: "all",
		Depends: []string{"luci-base", "olcwrt"},
		Section: "luci", License: "WTFPL", URL: projectURL, Maintainer: maintainer,
		Description: "LuCI interface for olcWRT (olcRTC client).",
		Files:       files,
		PostinstPkg: "#!/bin/sh\n[ -n \"${IPKG_INSTROOT}\" ] || {\n\trm -f /tmp/luci-indexcache.*\n\trm -rf /tmp/luci-modulecache/\n\t/etc/init.d/rpcd reload 2>/dev/null\n\texit 0\n}\n",
	}, nil
}

var langNames = map[string]string{"ru": "Русский (Russian)"}

func i18nSpecs(o *options, v versions) ([]*pkgSpec, error) {
	pos, _ := filepath.Glob(filepath.Join(o.root, "feed", "luci-app-olcwrt", "po", "*", "olcwrt.po"))
	var out []*pkgSpec
	for _, po := range pos {
		lang := filepath.Base(filepath.Dir(po))
		f, err := os.Open(po)
		if err != nil {
			return nil, err
		}
		lmo := compileLMO(parsePO(f))
		f.Close()
		if lmo == nil {
			continue
		}
		name := "luci-i18n-olcwrt-" + lang
		files := []pkgFile{{"/usr/lib/lua/luci/i18n/olcwrt." + lang + ".lmo", 0o644, lmo}}
		if ln := langNames[lang]; ln != "" {
			files = append(files, pkgFile{"/etc/uci-defaults/" + name, 0o644,
				[]byte(fmt.Sprintf("uci set luci.languages.%s='%s'; uci commit luci\n", strings.ReplaceAll(lang, "-", "_"), ln))})
		}
		out = append(out, &pkgSpec{
			Name: name, Version: v.luci, Arch: "all", Depends: []string{"luci-app-olcwrt"},
			Section: "luci", License: "WTFPL", URL: projectURL, Maintainer: maintainer,
			Description: "Translation for luci-app-olcwrt - " + lang,
			Files:       files,
		})
	}
	return out, nil
}

// buildAPKs packs every spec with `apk mkpkg`. The files are staged in a tar
// and unpacked by a helper script as root so ownership and modes are right,
// also when the apk binary runs inside WSL.
func buildAPKs(o *options, specs []*pkgSpec, mtime time.Time) error {
	stageDir := filepath.Join(o.out, "apk-stage")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	apkBin, viaWSL := strings.CutPrefix(o.apk, "wsl:")
	for _, s := range specs {
		stage, err := apkStage(s, mtime)
		if err != nil {
			return err
		}
		tarPath := filepath.Join(stageDir, s.Name+"_"+s.Arch+".tar")
		if err := os.WriteFile(tarPath, stage, 0o644); err != nil {
			return err
		}
		outDir := filepath.Join(o.out, "apk", s.Arch)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return err
		}
		out := filepath.Join(outDir, fmt.Sprintf("%s-%s.apk", s.Name, s.Version))
		path := func(p string) string {
			abs, _ := filepath.Abs(p)
			if viaWSL {
				return wslPath(abs)
			}
			return abs
		}
		args := s.apkMkpkgArgs(path(out))
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = shellQuote(a)
		}
		script := fmt.Sprintf("set -e\nw=$(mktemp -d)\ntrap 'rm -rf \"$w\"' EXIT\ntar -xpf %s -C \"$w\"\ncd \"$w\"\n%s %s\n",
			shellQuote(path(tarPath)), shellQuote(apkBin), strings.Join(quoted, " "))
		scriptPath := filepath.Join(stageDir, s.Name+"_"+s.Arch+".sh")
		if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
			return err
		}
		var cmd *exec.Cmd
		switch {
		case viaWSL:
			cmd = exec.Command("wsl", "-u", "root", "--", "sh", path(scriptPath))
		case runtime.GOOS == "linux" && os.Geteuid() != 0:
			cmd = exec.Command("fakeroot", "sh", scriptPath)
		default:
			cmd = exec.Command("sh", scriptPath)
		}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("apk mkpkg %s: %w", s.Name, err)
		}
		fmt.Println("apk:", rel(o.out, out))
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// wslPath converts C:\dir\file to /mnt/c/dir/file.
func wslPath(p string) string {
	p = filepath.ToSlash(p)
	if len(p) > 1 && p[1] == ':' {
		return "/mnt/" + strings.ToLower(p[:1]) + p[2:]
	}
	return p
}
