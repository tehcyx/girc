.PHONY: build

VERSION=$(shell cat VERSION)
GIT_COMMIT=$(shell git rev-list -1 HEAD)

build: test cover
	go build -ldflags "-X github.com/tehcyx/girc/pkg/version.Version=${VERSION} -X github.com/tehcyx/girc/pkg/version.GitCommit=${GIT_COMMIT}" -o bin/app cmd/girc/main.go

docker:
	CGO_ENABLED=0 GOOS=linux go build -ldflags "-X github.com/tehcyx/girc/pkg/version.Version=${VERSION} -X github.com/tehcyx/girc/pkg/version.GitCommit=${GIT_COMMIT} -s" -a -installsuffix cgo -o bin/appdocker cmd/girc/main.go
	docker build -t girc .

run: docker
	docker run --rm -p 6665:6665 girc

test:
	go test ./...

cover:
	go test ./... -cover

clean:
	rm -rf bin
