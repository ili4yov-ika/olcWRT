package main

import (
	"fmt"
	"sort"
	"strings"
)

// goTarget is one distinct Go cross-compilation setting.
type goTarget struct {
	GOARCH string
	Env    []string // GOARM, GOMIPS, GO386 ...
}

func (t goTarget) key() string {
	return strings.Join(append([]string{t.GOARCH}, t.Env...), "_")
}

// archTable maps OpenWrt package architectures to Go targets. Go binaries
// are static, so every CPU of a family shares one build; soft-float is used
// wherever the architecture name does not promise an FPU.
var archTable = map[string]goTarget{
	"aarch64_cortex-a53":        {"arm64", nil},
	"aarch64_cortex-a72":        {"arm64", nil},
	"aarch64_cortex-a76":        {"arm64", nil},
	"aarch64_generic":           {"arm64", nil},
	"arm_arm1176jzf-s_vfp":      {"arm", []string{"GOARM=6"}},
	"arm_arm926ej-s":            {"arm", []string{"GOARM=5"}},
	"arm_cortex-a15_neon-vfpv4": {"arm", []string{"GOARM=7"}},
	"arm_cortex-a5_vfpv4":       {"arm", []string{"GOARM=7"}},
	"arm_cortex-a7":             {"arm", []string{"GOARM=5"}},
	"arm_cortex-a7_neon-vfpv4":  {"arm", []string{"GOARM=7"}},
	"arm_cortex-a7_vfpv4":       {"arm", []string{"GOARM=7"}},
	"arm_cortex-a8_vfpv3":       {"arm", []string{"GOARM=7"}},
	"arm_cortex-a9":             {"arm", []string{"GOARM=5"}},
	"arm_cortex-a9_neon":        {"arm", []string{"GOARM=7"}},
	"arm_cortex-a9_vfpv3-d16":   {"arm", []string{"GOARM=7"}},
	"arm_fa526":                 {"arm", []string{"GOARM=5"}},
	"arm_xscale":                {"arm", []string{"GOARM=5"}},
	"i386_pentium-mmx":          {"386", []string{"GO386=softfloat"}},
	"i386_pentium4":             {"386", []string{"GO386=sse2"}},
	"loongarch64_generic":       {"loong64", nil},
	"mips64_mips64r2":           {"mips64", []string{"GOMIPS64=softfloat"}},
	"mips64_octeonplus":         {"mips64", []string{"GOMIPS64=softfloat"}},
	"mips64el_mips64r2":         {"mips64le", []string{"GOMIPS64=softfloat"}},
	"mips_24kc":                 {"mips", []string{"GOMIPS=softfloat"}},
	"mips_4kec":                 {"mips", []string{"GOMIPS=softfloat"}},
	"mips_mips32":               {"mips", []string{"GOMIPS=softfloat"}},
	"mipsel_24kc":               {"mipsle", []string{"GOMIPS=softfloat"}},
	"mipsel_24kc_24kf":          {"mipsle", []string{"GOMIPS=hardfloat"}},
	"mipsel_74kc":               {"mipsle", []string{"GOMIPS=softfloat"}},
	"mipsel_mips32":             {"mipsle", []string{"GOMIPS=softfloat"}},
	"riscv64_generic":           {"riscv64", nil},
	"riscv64_riscv64":           {"riscv64", nil},
	"x86_64":                    {"amd64", nil},
}

// commonArchs covers the vast majority of current routers.
var commonArchs = []string{
	"aarch64_cortex-a53", "aarch64_cortex-a72", "aarch64_generic",
	"arm_cortex-a7_neon-vfpv4", "arm_cortex-a9", "arm_cortex-a9_vfpv3-d16", "arm_cortex-a15_neon-vfpv4",
	"mipsel_24kc", "mips_24kc", "x86_64",
}

func resolveArchs(spec string) ([]string, error) {
	switch spec {
	case "", "common":
		return commonArchs, nil
	case "all":
		var all []string
		for a := range archTable {
			all = append(all, a)
		}
		sort.Strings(all)
		return all, nil
	}
	var out []string
	for _, a := range strings.Split(spec, ",") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if _, ok := archTable[a]; !ok {
			return nil, fmt.Errorf("unknown architecture %q (see `opkg print-architecture` or DISTRIB_ARCH in /etc/openwrt_release)", a)
		}
		out = append(out, a)
	}
	return out, nil
}
