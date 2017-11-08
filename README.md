# Kubectl Experiment

This project contains an expderiment of pulling kubectl out of the Kubernetes repo.

## Get the project

This codebase should be checked out into your `GOPATH` at `k8s.io/kubectl` for
it to work.

## Get dependencies

Run `make deps` to install the dependencies to the right place. It uses the
dependency manager dep plus some scripts to clean up the dependencies.

## Steps I've done

1. Copied `kubernetes/kubernetes/cmd/kubectl` to this directory.
2. Run `dep init` and then worked through the version issues. Use `made deps` to ensure all the right things are done.
3. Copied `kubernetes/kubernetes/pkg/kubectl` to `kubectl/pkg` along with proper renaming.

## Gotchas

* The tests currently don't pass. For example, there are files, such as openapi specs, needed for the tests that are in the kubernetes repo. They have not yet been dealt with. Need to decide if these files will be linked to in the vendor directory, if they should be moved over to a repo like this, or something else.