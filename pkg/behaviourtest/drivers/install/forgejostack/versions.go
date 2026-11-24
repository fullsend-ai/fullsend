package forgejostack

// The pinned components of the managed forgejo-dev stack. Bump the
// Forgejo and Forgejo Runner versions here, together; nothing else
// names them.
const (
	// ForgejoVersion is the pinned upstream Forgejo release.
	ForgejoVersion = "17.0.1"

	// RunnerVersion is the pinned Forgejo Runner release.
	RunnerVersion = "13.2.1"

	// ForgejoImage is the rootless Forgejo server image. The rootless
	// variant runs no SSH daemon, so it binds nothing on the host network
	// besides the HTTP port the stack picks.
	ForgejoImage = "codeberg.org/forgejo/forgejo:" + ForgejoVersion + "-rootless"

	// RunnerImage is the Forgejo Runner image.
	RunnerImage = "data.forgejo.org/forgejo/runner:" + RunnerVersion

	// JobImage is the container image the runner's labels map to: the
	// image Actions jobs of the stack run in. It is fully qualified so
	// Podman never prompts for a short-name registry.
	JobImage = "docker.io/library/node:22-bookworm"

	// RunnerCapacity is the number of Actions jobs the runner executes
	// concurrently, matching the suite's initial scenario concurrency.
	RunnerCapacity = 2
)

// runnerLabels are the labels the runner registers with: each maps to
// JobImage, so a job's runs-on selects a container job either way.
var runnerLabels = []string{
	"docker:docker://" + JobImage,
	"ubuntu-latest:docker://" + JobImage,
}
