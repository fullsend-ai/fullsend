package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// hostFileShape is what classifyHostFile finds in a host_files source.
type hostFileShape string

const (
	hostFileServiceAccount  hostFileShape = "service_account"
	hostFileAuthorizedUser  hostFileShape = "authorized_user"
	hostFileExternalAccount hostFileShape = "external_account"
	hostFileImpersonatedSA  hostFileShape = "impersonated_service_account"
	hostFilePrivateKey      hostFileShape = "private_key"
	hostFileUnknown         hostFileShape = "unknown"
)

// maxHostFileClassifyBytes bounds how much of a host file is read to
// classify it. Credential files are far smaller.
const maxHostFileClassifyBytes = 64 << 10

// pemPrivateKeyRe matches the header of a PEM private key block, such as
// PRIVATE KEY, RSA PRIVATE KEY, EC PRIVATE KEY or OPENSSH PRIVATE KEY.
var pemPrivateKeyRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)

// durable reports whether a credential of this shape outlives the run: the
// run neither issued it nor can revoke it once it is in the sandbox.
// external_account files are short-lived by construction, and unknown files
// are not treated as credentials.
func (s hostFileShape) durable() bool {
	switch s {
	case hostFileServiceAccount, hostFileAuthorizedUser, hostFileImpersonatedSA, hostFilePrivateKey:
		return true
	}
	return false
}

// describe names the shape for run output.
func (s hostFileShape) describe() string {
	switch s {
	case hostFileServiceAccount:
		return "service_account key"
	case hostFilePrivateKey:
		return "PEM private key"
	}
	return string(s) + " credential"
}

// classifyHostFile reads the start of the host file at path and returns its
// credential shape. It runs on the host before upload; nothing inspects the
// file inside the sandbox. A file that is empty or not a recognized
// credential is hostFileUnknown. An error means the file could not be read.
func classifyHostFile(path string) (hostFileShape, error) {
	f, err := os.Open(path)
	if err != nil {
		return hostFileUnknown, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxHostFileClassifyBytes))
	if err != nil {
		return hostFileUnknown, err
	}
	return classifyHostFileContent(data), nil
}

// classifyHostFileContent returns the credential shape of data.
func classifyHostFileContent(data []byte) hostFileShape {
	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("{")) {
		var creds hostFileCredentialJSON
		if json.Unmarshal(trimmed, &creds) == nil {
			if shape := creds.shape(); shape != hostFileUnknown {
				return shape
			}
		}
	}
	if pemPrivateKeyRe.Match(data) {
		return hostFilePrivateKey
	}
	return hostFileUnknown
}

// hostFileCredentialJSON is the part of a Google credential file that
// decides its shape. Impersonation files nest their source in
// source_credentials.
type hostFileCredentialJSON struct {
	Type              string                  `json:"type"`
	SourceCredentials *hostFileCredentialJSON `json:"source_credentials"`
}

// shape returns the credential shape. An impersonated_service_account file
// lasts only as long as its source, so one whose source is an
// external_account is classified as external_account.
func (c *hostFileCredentialJSON) shape() hostFileShape {
	switch shape := hostFileShape(strings.TrimSpace(c.Type)); shape {
	case hostFileServiceAccount, hostFileAuthorizedUser, hostFileExternalAccount:
		return shape
	case hostFileImpersonatedSA:
		if c.SourceCredentials != nil && c.SourceCredentials.shape() == hostFileExternalAccount {
			return hostFileExternalAccount
		}
		return shape
	}
	return hostFileUnknown
}

// checkHostFileCredentials classifies each host_files source the run will
// copy into the sandbox. It returns one report line per mount of a durable
// credential that the harness opted into with allow_durable: true, and an
// error naming every durable mount that lacks the opt-in. Sources that are
// skipped or cannot be read are left to bootstrapEnv, which reports them
// when it copies host files.
func checkHostFileCredentials(h *harness.Harness) ([]string, error) {
	var lines, errs []string
	for i, hf := range h.HostFiles {
		path, copied := hostFileSource(hf)
		if !copied {
			continue
		}
		shape, err := classifyHostFile(path)
		if err != nil || !shape.durable() {
			continue
		}
		if !hf.AllowDurable {
			errs = append(errs, fmt.Sprintf(
				"host_files[%d]: host file %s (dest %s): %s, whose lifetime this run does not control; set allow_durable: true on the entry to mount it",
				i, path, hf.Dest, shape.describe()))
			continue
		}
		lines = append(lines, fmt.Sprintf("host file %s: %s (lifetime not controlled by this run)", path, shape.describe()))
	}
	if len(errs) > 0 {
		return lines, fmt.Errorf("%d durable credential host file(s) without allow_durable:\n    %s", len(errs), strings.Join(errs, "\n    "))
	}
	return lines, nil
}
