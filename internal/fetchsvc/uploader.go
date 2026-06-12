package fetchsvc

import "github.com/fullsend-ai/fullsend/internal/sandbox"

// SandboxUploader implements the Uploader interface by delegating to
// sandbox.UploadDir for tarball-based directory transfer into the sandbox.
type SandboxUploader struct{}

func (u *SandboxUploader) UploadSkillDir(sandboxName, localPath, remotePath string) error {
	return sandbox.UploadDir(sandboxName, localPath, remotePath)
}
