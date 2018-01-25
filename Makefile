default: builddocker

buildgo:
	go build -i

builddocker:
	CGO_ENABLED=0 GOOS=linux go build -ldflags "-s" -a -installsuffix cgo -o gircdocker
	docker build -t girc .

run:
	docker run --rm -p 6665:6665 girc

test:
	go test ./...

cover:
	go test ./.. -cover