.PHONY: verify test demo bench
verify:
	./scripts/verify.sh origin/master
test:
	go test -race ./...
	 node --test examples/lime2-demo/client.test.mjs scripts/diff-coverage.test.mjs
demo:
	go run ./examples/lime2-demo
bench:
	go test -run '^$$' -bench . -benchmem -count=3 .
