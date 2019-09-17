default: build

build: test cover
	go build -i -o bin/app

docker:
	CGO_ENABLED=0 GOOS=linux go build -ldflags "-s" -a -installsuffix cgo -o bin/appdocker
	docker build -t girc .

run: docker
	docker run --rm -p 6665:6665 girc

test:
	go test ./...

cover:
	go test ./... -cover

clean:
	rm -rf bin
