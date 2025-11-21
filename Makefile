# ---- configuration ----
BINARY_NAME := coral
INSTALL_DIR := /usr/local/bin

BASH_COMPLETION_DIR := /usr/share/bash-completion/completions
ZSH_COMPLETION_DIR := /usr/share/zsh/site-functions
FISH_COMPLETION_DIR := /usr/share/fish/completions

# ---- build ----
.PHONY: all
all: build

.PHONY: build
build:
	@echo "==> Building $(BINARY_NAME)..."
	@mkdir -p bin
	@go build -o bin/$(BINARY_NAME)
	@echo "==> Wrote to ./bin/$(BINARY_NAME)"

.PHONY: clean
clean:
	@echo "==> Cleaning..."
	@rm -rf bin
	@echo "==> Done."

# ---- install ----
.PHONY: install
install: build
	@echo "==> Installing binary to $(INSTALL_DIR)..."
	@sudo install -Dm755 bin/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)

	@echo "==> Installing shell completions..."
	@sudo mkdir -p $(BASH_COMPLETION_DIR)
	@sudo mkdir -p $(ZSH_COMPLETION_DIR)
	@sudo mkdir -p $(FISH_COMPLETION_DIR)
	@sudo $(INSTALL_DIR)/$(BINARY_NAME) completion bash | \
		sudo tee $(BASH_COMPLETION_DIR)/$(BINARY_NAME) > /dev/null
	@sudo $(INSTALL_DIR)/$(BINARY_NAME) completion zsh | \
		sudo tee $(ZSH_COMPLETION_DIR)/_$(BINARY_NAME) > /dev/null
	@sudo $(INSTALL_DIR)/$(BINARY_NAME) completion fish | \
		sudo tee $(FISH_COMPLETION_DIR)/$(BINARY_NAME).fish > /dev/null
	@echo "==> Autocompletion installed."
	@echo "To enable it in the current terminal, run:"
	@echo "  Bash: source $(BASH_COMPLETION_DIR)/$(BINARY_NAME)"
	@echo "  Zsh:  source $(ZSH_COMPLETION_DIR)/_$(BINARY_NAME)"
	@echo "  Fish: source $(FISH_COMPLETION_DIR)/$(BINARY_NAME).fish"
	@echo "or open a new terminal."
	@echo "==> Installation complete."

# ---- uninstall ----
.PHONY: uninstall
uninstall:
	@echo "==> Removing binary..."
	@sudo rm -f $(INSTALL_DIR)/$(BINARY_NAME)

	@echo "==> Removing shell completions..."
	@sudo rm -f $(BASH_COMPLETION_DIR)/$(BINARY_NAME)
	@sudo rm -f $(ZSH_COMPLETION_DIR)/_$(BINARY_NAME)
	@sudo rm -f $(FISH_COMPLETION_DIR)/$(BINARY_NAME).fish

	@echo "==> Uninstall complete."
