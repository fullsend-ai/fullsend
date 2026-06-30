package binary

import (
	"debug/elf"
	"fmt"
)

// DefaultArch is the architecture used for vendored binaries (linux/amd64 GHA runners).
const DefaultArch = "amd64"

var validArchs = map[string]bool{"amd64": true, "arm64": true}

// ValidateLinuxBinary checks that the file at path is a Linux ELF executable
// for the expected architecture. Returns a descriptive error if the file is
// missing, not ELF, not Linux, or the wrong architecture.
func ValidateLinuxBinary(path, arch string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("not a valid ELF binary (is this a macOS Mach-O?): %w", err)
	}
	defer f.Close()

	if f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
		return fmt.Errorf("ELF OS/ABI is %s, expected Linux or NONE", f.OSABI)
	}

	archToMachine := map[string]elf.Machine{
		"amd64": elf.EM_X86_64,
		"arm64": elf.EM_AARCH64,
	}
	if expected, ok := archToMachine[arch]; ok && f.Machine != expected {
		return fmt.Errorf("ELF machine is %s, expected %s for %s", f.Machine, expected, arch)
	}
	return nil
}

// ValidArch reports whether arch is a supported linux target (amd64 or arm64).
func ValidArch(arch string) bool {
	return validArchs[arch]
}
