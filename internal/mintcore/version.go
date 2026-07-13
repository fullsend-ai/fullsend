package mintcore

// Version and Commit are stamped into the Cloud Function source at
// deployment time by the provisioner. In development (local dev, tests)
// they default to empty strings.
var (
	Version string
	Commit  string
)
