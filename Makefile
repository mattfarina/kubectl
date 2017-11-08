# `make deps` will try to ensure the right versions of dependencies are used.
# It will use dep to put the dependencies in the proper place with close to
# proper versions. This works for external dependencies. For those coupled to
# the kubernetes/kubernetes repo, they are coppied to the right place rather
# than being pulled from upstream. This can be removed when the sync out of
# Kubernetes is working for app developers.
.PHONY: deps
deps:
	go get -u github.com/golang/dep/cmd/dep
	dep ensure

	rm -rf ./vendor/k8s.io/{api,apiextensions-apiserver,apimachinery,apiserver,client-go,metrics}

	cp -r ./vendor/k8s.io/kubernetes/staging/src/k8s.io/{api,apiextensions-apiserver,apimachinery,apiserver,client-go,metrics} ./vendor/k8s.io
