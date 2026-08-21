.PHONY: test vet e2e clean-artifacts

test:
	go test ./...

vet:
	go vet ./...

e2e:
	./scripts/run-e2e.sh

clean-artifacts:
	find artifacts -mindepth 1 -maxdepth 1 -type d -mtime +14 -exec rm -rf -- {} +
