APP_NAME := simple-wallpaper
BUILD_DIR := dist
BIN := $(BUILD_DIR)/$(APP_NAME)

GO := go
GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: all prod build run test clean deps verify

all: prod

prod: deps verify test
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN) .
	@echo "Built optimized binary: $(BIN)"
	@du -h $(BIN) 2>/dev/null || true

build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -o $(BIN) .
	@echo "Built debug/dev binary: $(BIN)"

run:
	$(GO) run .

test:
	$(GO) test ./...

deps:
	$(GO) mod download

verify:
	$(GO) mod verify

clean:
	rm -rf $(BUILD_DIR)
