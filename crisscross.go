package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type VendorToolchain struct {
	URL           string
	AliasFor      string
	LocationAfter string
	BinPrefix     string
}

var TOOLCHAIN_VENDORS []byte

func getToolchainVendors() {
	if d, err := os.ReadFile("./CrissCross/ToolchainVendors"); err == nil {
		TOOLCHAIN_VENDORS = d
	} else {
		resp, err := http.Get("https://files.tomtebender.de/CrissCross/ToolchainVendors")
		if err != nil {
			panic(err)
		}
		if resp.StatusCode != http.StatusOK {
			panic("Error while retrieving https://files.tomtebender.de/CrissCross/ToolchainVendors! If this issue persists, please add a vendor file at ./CrissCross/ToolchainVendors")
		}
		TOOLCHAIN_VENDORS, err = io.ReadAll(resp.Body)
		if err != nil {
			panic(err)
		}
	}
	TOOLCHAIN_VENDORS = append(TOOLCHAIN_VENDORS, '\n')
}

func parseToolchainVendors() (Vendored map[string]VendorToolchain) {
	Vendored = map[string]VendorToolchain{}

	for {
		idx := bytes.IndexByte(TOOLCHAIN_VENDORS, '\n')
		if idx == -1 {
			break
		}
		ln := TOOLCHAIN_VENDORS[:idx]
		TOOLCHAIN_VENDORS = TOOLCHAIN_VENDORS[idx+1:]
		if bytes.Equal(ln, []byte{}) {
			continue
		}
		if ln[0] == '#' {
			continue
		}
		vt := VendorToolchain{}
		var name string
		spl := bytes.Split(ln, []byte{'\t'})
		if bytes.Equal(spl[0], []byte{'+'}) {
			name = string(spl[1])
			vt.AliasFor = string(spl[2])
		} else {
			name = string(spl[0])
			vt.URL = string(spl[1])
			vt.LocationAfter = string(spl[2])
			vt.BinPrefix = string(spl[3])
		}
		Vendored[name] = vt
	}
	TOOLCHAIN_VENDORS = nil
	return
}

type CrissCrossTarget struct {
	Type     string   `yaml:"type"`
	Src      []string `yaml:"src"`
	Include  []string `yaml:"include"`
	Requires []string `yaml:"requires"`
	Ldflags  string   `yaml:"ldflags"`
	Static   bool     `yaml:"static"`
}

type CrissCrossYML struct {
	Toolchains []string                    `yaml:"toolchains"`
	Targets    map[string]CrissCrossTarget `yaml:"targets"`
}

func cmd_init() {
	getToolchainVendors()
	err := os.MkdirAll("./CrissCross/toolchains", 0750)
	if err != nil {
		panic(err)
	}
	err = os.WriteFile("./CrissCross/ToolchainVendors", TOOLCHAIN_VENDORS, 0750)
	if err != nil {
		panic(err)
	}
	if !exists("./CrissCross.yml") {

		os.WriteFile("./CrissCross.yml", []byte(
			`toolchains:
#  - x86_64-linux-gnu

targets:
#  example:
#    type: elf # options: elf, o, so, ar
#    static: true
#    src:
#      - korn/lifeIsPeachy/killYou.cpp
#      - static_x/start_a_war/set_it_off.c
#    include:
#      - exampleincludedir
#    requires:
#      - examplelib
#    ldflags: -o2
#
#  examplelib:
#    type: ar
#    src:
#      - placebo/placebo/nancyBoy.c
`), 0750)
	}
	fmt.Printf("CrissCross: Initialized CrissCross project in '%s'\n", os.Getenv("PWD"))
	os.Exit(0)
}

func cmd_make(bs bool) {
	yml := CrissCrossYML{}
	{
		yb, err := os.ReadFile("./CrissCross.yml")
		if err != nil {
			panic(err)
		}
		err = yaml.Unmarshal(yb, &yml)
		if err != nil {
			panic(err)
		}
	}
	if d, err := os.ReadFile("./CrissCross/ToolchainVendors"); err == nil {
		TOOLCHAIN_VENDORS = d
	} else {
		getToolchainVendors()
	}
	ven := parseToolchainVendors()

	err := os.MkdirAll("./CrissCross/toolchains", 0750)
	if err != nil {
		panic(err)
	}

	err = os.MkdirAll("./.tmp", 0750)
	if err != nil {
		panic(err)
	}
	for _, tc := range yml.Toolchains {
		t, ok := ven[tc]
		if !ok {
			fmt.Printf("CrissCross: Unknown toolchain '%s'\n", tc)
			os.Exit(1)
		}
		url := t.URL
		loc := "./CrissCross/toolchains/" + t.LocationAfter
		if t.AliasFor != "" {
			tt, ok := ven[t.AliasFor]
			if !ok {
				fmt.Printf("CrissCross: Internal error: Alias '%s' references unknown toolchain '%s'", tc, t.AliasFor)
			}
			url = tt.URL
			loc = "./CrissCross/toolchains/" + tt.LocationAfter
			ven[tc] = ven[t.AliasFor]
		}
		if !exists(loc) {
			var filename string
			{
				spl := strings.Split(strings.TrimSuffix(url, "/"), "/")
				filename = "./.tmp/" + spl[len(spl)-1]
			}
			fmt.Printf("Downloading toolchain %s: %s ...\n", tc, url)
			resp, err := http.Get(url)
			if err != nil {
				panic(err)
			}

			out, err := os.Create(filename)
			if err != nil {
				panic(err)
			}
			defer out.Close()
			_, err = io.Copy(out, resp.Body)

			fmt.Println("Extracting...")

			// i am aware that this is the "dirty" variant of extracting, but it significantly simplifies this
			cmd := exec.Command("tar", "-xf", filename, "-C", "./CrissCross/toolchains")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run()
		}
	}
	os.RemoveAll("./.tmp")
	if bs {
		return
	}

	makefile_first := "# please only use absolute paths for the builddir\nBUILDDIR=${PWD}/BUILD\n\nall:"
	var makefile_second string = ""

	for tname, target := range yml.Targets {
		makefile_first = makefile_first + " " + tname

		maintarget := "\n\n" + tname + ":"
		var archtargets string = ""

		var optargs string = ""
		if target.Type == "so" {
			optargs += "-fPIC "
		}
		if target.Include != nil {
			for _, inc := range target.Include {
				optargs = optargs + "-I" + inc + " "
			}
		}
		if target.Ldflags != "" {
			optargs = optargs + target.Ldflags + " "
		}

		c_src := []string{}
		cpp_src := []string{}
		for _, srcf := range target.Src {
			if strings.HasSuffix(srcf, ".c") {
				c_src = append(c_src, srcf)
			} else if strings.HasSuffix(srcf, ".cpp") {
				cpp_src = append(cpp_src, srcf)
			} else if strings.HasSuffix(srcf, ".h") || strings.HasSuffix(srcf, ".hpp") {
			} else {
				fmt.Printf("CrissCross: Invalid file ending for file '%s' (CrissCross only supports C & C++ currently)\n", srcf)
				os.Exit(1)
			}
		}

		if (target.Type == "a" || target.Type == "so") && !strings.HasPrefix(tname, "lib") {
			fmt.Printf("CrissCross: warning: library target '%s' (%s) does not start with 'lib'\n", tname, target.Type)
		}

		for _, tcname := range yml.Toolchains {
			tc := ven[tcname]
			maintarget = fmt.Sprintf("%s %s_%s", maintarget, tname, tcname)
			var reqs string = ""
			if target.Requires != nil {
				for _, req := range target.Requires {
					reqs = fmt.Sprintf("%s %s_%s", reqs, req, tcname)
				}
			}
			archtargets = fmt.Sprintf("%s\n\n%s_%s:%s\n\t@mkdir -p ${BUILDDIR}/%s generated/%s generated/lib/%s\n", archtargets, tname, tcname, reqs, tcname, tcname, tcname)

			////////////

			for _, src := range c_src {
				archtargets = fmt.Sprintf("%s\tCrissCross/toolchains/%s%sgcc -o generated/%s/%s_%s.o -L./generated/lib/%s %s -c %s\n",
					archtargets,
					tc.LocationAfter, tc.BinPrefix,
					tcname,
					tname,
					filepath.Base(src),
					tcname,
					optargs,
					src)
			}
			for _, src := range cpp_src {
				archtargets = fmt.Sprintf("%s\tCrissCross/toolchains/%s%sg++ -o generated/%s/%s_%s.o -L./generated/lib/%s %s -c %s\n",
					archtargets,
					tc.LocationAfter, tc.BinPrefix,
					tcname,
					tname,
					filepath.Base(src),
					tcname,
					optargs,
					src)
			}

			////////////

			finalcmd := "g++ "
			if slices.Equal(cpp_src, []string{}) {
				finalcmd = "gcc "
			}
			if target.Static {
				finalcmd += "-static "
			}
			switch target.Type {
			case "elf":
				finalcmd += fmt.Sprintf("-o ${BUILDDIR}/%s/%s.elf generated/%s/%s*.o", tcname, tname, tcname, tname)
				archtargets = fmt.Sprintf("%s\n\tCrissCross/toolchains/%s%s%s", archtargets, tc.LocationAfter, tc.BinPrefix, finalcmd)
			case "so":
				finalcmd += fmt.Sprintf("-shared -o ${BUILDDIR}/%s/%s.so generated/%s/%s*.o\n\tln -sf ${BUILDDIR}/%s/%s.so generated/lib/%s/", tcname, tname, tcname, tname, tcname, tname, tcname)
				archtargets = fmt.Sprintf("%s\n\tCrissCross/toolchains/%s%s%s", archtargets, tc.LocationAfter, tc.BinPrefix, finalcmd)
			case "a":
				archtargets = fmt.Sprintf("%s\n\tar rcs ${BUILDDIR}/%s/%s.a\n\tln -sf ${BUILDDIR}/%s/%s.a generated/lib/%s/", archtargets, tcname, tname, tcname, tname, tcname)
			}
		}

		makefile_second = makefile_second + "\n" + maintarget + archtargets
	}
	err = os.WriteFile("./Makefile", []byte(makefile_first+`
	@echo "\033[38;2;255;0;127m♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ \n\
	                                  \033[38;2;0;255;0mYour build finished succesfully! ☺ \n\n\
	             \033[38;2;127;0;255mThis Makefile was automatically generated using the CrissCross build system. \n\
	Please check it out at https://codeberg.org/tomteb/CrissCross. CrissCross depends on your support! \033[38;2;255;0;127m\033[5m♥\033[25m \n\
	♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡ ♡\\033[0m"`+makefile_second+`

clean:
	@rm -rvf ${BUILDDIR} generated

distclean: clean
	@rm -vf Makefile
`), 0640)
	if err != nil {
		panic(err)
	}

	fmt.Println("CrissCross: Run 'make' now to build.")

}

func main() {
	flag_bootstrap := flag.Bool("bootstrap", false, "")
	flag.Parse()
	switch flag.Arg(0) {
	case "init":
		cmd_init()
	case "":
		cmd_make(*flag_bootstrap)
	default:
		fmt.Printf("CrissCross: Unknown command '%s'\n", flag.Arg(0))
		os.Exit(1)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
