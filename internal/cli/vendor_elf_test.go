package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/binary"
)

// minimalLinuxAmd64ELF is a 64-byte ELF64 header that debug/elf.Open
// accepts (no program or section headers). Machine is EM_X86_64 so it
// satisfies vendorArch validation on any host, including arm64.
//
// Layout: e_ident[16], e_type, e_machine, e_version, e_entry, e_phoff,
// e_shoff, e_flags, e_ehsize, e_phentsize, e_phnum, e_shentsize, e_shnum,
// e_shstrndx.
var minimalLinuxAmd64ELF = []byte{
	0x7f, 'E', 'L', 'F',
	2, // ELFCLASS64
	1, // ELFDATA2LSB
	1, // EV_CURRENT
	0, // ELFOSABI_NONE
	0, 0, 0, 0, 0, 0, 0, 0,
	0x02, 0x00, // ET_EXEC
	0x3e, 0x00, // EM_X86_64
	0x01, 0x00, 0x00, 0x00, // EV_CURRENT
	0, 0, 0, 0, 0, 0, 0, 0, // e_entry
	0, 0, 0, 0, 0, 0, 0, 0, // e_phoff
	0, 0, 0, 0, 0, 0, 0, 0, // e_shoff
	0, 0, 0, 0, // e_flags
	0x40, 0x00, // e_ehsize = 64
	0x38, 0x00, // e_phentsize = 56
	0x00, 0x00, // e_phnum
	0x40, 0x00, // e_shentsize = 64
	0x00, 0x00, // e_shnum
	0x00, 0x00, // e_shstrndx
}

// amd64VendorBinary writes a linux/amd64 ELF fixture and returns its path.
// Vendoring always validates against binary.DefaultArch ("amd64"); tests
// must not pass os.Executable(), which is the host arch.
func amd64VendorBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fullsend")
	require.NoError(t, os.WriteFile(path, minimalLinuxAmd64ELF, 0o755))
	require.NoError(t, binary.ValidateLinuxBinary(path, binary.DefaultArch))
	return path
}

func TestAmd64VendorBinary_ValidatesOnAnyHost(t *testing.T) {
	path := amd64VendorBinary(t)
	require.NoError(t, binary.ValidateLinuxBinary(path, "amd64"))
	err := binary.ValidateLinuxBinary(path, "arm64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EM_X86_64")
	assert.Contains(t, err.Error(), "EM_AARCH64")
}
