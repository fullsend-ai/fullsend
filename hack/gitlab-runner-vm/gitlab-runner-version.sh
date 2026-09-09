#!/usr/bin/env bash
# Central pin for the gitlab-runner version used by create-openshift-vm.sh, create-gcp-vm.sh, and setup.sh.
# Update this single file to change the version everywhere.
GITLAB_RUNNER_VERSION="${GITLAB_RUNNER_VERSION:-19.2.1}"
