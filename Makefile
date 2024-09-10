test:
	go test -coverprofile cp.out ./...

test-coverage: test
	go tool cover -func cp.out
