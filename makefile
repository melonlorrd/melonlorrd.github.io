build:
	@go run main.go

clean:
	@rm -rf build/

serve:
	@rm -rf build/
	@go run main.go
	@python3 -m http.server -d build