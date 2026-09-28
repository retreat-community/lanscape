# Lanscape Mini build. Cross-compiles with zig cc (musl, static).
#   make TARGET=linux-mipsle        build lsm-agent and lsm-server
#   make test                       unit and property tests on the host
#   make size-check TARGET=...      enforce the size budget
ZIG ?= zig
TARGET ?= linux-amd64
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
OBJCOPY ?= $(shell command -v llvm-objcopy || command -v llvm-objcopy-18 || command -v llvm-objcopy-17 || true)

T_linux-amd64    := x86_64-linux-musl
T_linux-386      := x86-linux-musl
T_linux-arm64    := aarch64-linux-musl
T_linux-armv7    := arm-linux-musleabihf -mcpu=generic+v7a+vfp3d16
T_linux-armv6    := arm-linux-musleabihf -mcpu=arm1176jzf_s
T_linux-armv5    := arm-linux-musleabi -mcpu=generic+v5te+soft_float
T_linux-mips     := mips-linux-musleabi -mcpu=mips32r2+soft_float
T_linux-mipsle   := mipsel-linux-musleabi -mcpu=mips32r2+soft_float
T_linux-mips64   := mips64-linux-muslabi64 -Wno-option-ignored
T_linux-mips64le := mips64el-linux-muslabi64 -Wno-option-ignored
T_linux-riscv64  := riscv64-linux-musl
T_linux-ppc64le  := powerpc64le-linux-musl
T_freebsd-amd64  := x86_64-freebsd
TRIPLE := $(T_$(TARGET))
ifeq ($(TRIPLE),)
$(error unknown TARGET $(TARGET))
endif

TARGETS := linux-amd64 linux-386 linux-arm64 linux-armv7 linux-armv6 linux-armv5 linux-mips \
	linux-mipsle linux-mips64 linux-mips64le linux-riscv64 linux-ppc64le freebsd-amd64

CFLAGS := -std=c11 -D_GNU_SOURCE -Os -ffunction-sections -fdata-sections \
	-fno-asynchronous-unwind-tables -fno-stack-protector -Wall -Wextra -Werror \
	-DLS_VERSION=\"$(VERSION)\"
LDFLAGS := -static -Wl,--gc-sections -s
STRIP_SECTIONS := --remove-section=.comment --remove-section=.pdr --remove-section=.mdebug.abi32 \
	--remove-section=.gnu.attributes --remove-section=.note.gnu.build-id

BUILD := build/$(TARGET)
GEN := build/gen
COMMON := common/sha256.c common/tlv.c common/util.c
AGENT_SRC := agent/main.c agent/dp.c agent/icmp.c common/netif.c $(COMMON)
SERVER_SRC := server/main.c server/agents.c server/run.c server/segments.c server/store.c \
	server/http.c server/httpparse.c common/jw.c $(COMMON)
HDRS := $(wildcard common/*.h agent/*.h server/*.h)

.PHONY: all agent server test size-check page clean lint fuzz all-targets

all: agent server

agent: $(BUILD)/lsm-agent
server: $(BUILD)/lsm-server
page: $(GEN)/index.html.gz

$(BUILD)/lsm-agent: $(AGENT_SRC) $(HDRS)
	@mkdir -p $(BUILD)
	$(ZIG) cc -target $(TRIPLE) $(CFLAGS) $(LDFLAGS) -o $@ $(AGENT_SRC)
	@if [ -n "$(OBJCOPY)" ]; then $(OBJCOPY) $(STRIP_SECTIONS) $@; fi

$(BUILD)/lsm-server: $(SERVER_SRC) $(HDRS) $(GEN)/index_html_gz.h
	@mkdir -p $(BUILD)
	$(ZIG) cc -target $(TRIPLE) $(CFLAGS) -I$(GEN) $(LDFLAGS) -o $@ $(SERVER_SRC)
	@if [ -n "$(OBJCOPY)" ]; then $(OBJCOPY) $(STRIP_SECTIONS) $@; fi

$(GEN)/bin2c: tools/bin2c.c
	@mkdir -p $(GEN)
	$(ZIG) cc -O2 -o $@ $<

$(GEN)/index.html.gz: web/index.html
	@mkdir -p $(GEN)
	gzip -9n -c $< > $@

$(GEN)/index_html_gz.h: $(GEN)/index.html.gz $(GEN)/bin2c
	$(GEN)/bin2c $< index_html_gz > $@

size-check: all page
	@./tools/size-check.sh $(BUILD) $(TARGET)

all-targets:
	@for t in $(TARGETS); do $(MAKE) --no-print-directory TARGET=$$t all || exit 1; done

TEST_CFLAGS := -std=c11 -D_GNU_SOURCE -O1 -g -Wall -Wextra -Werror -fsanitize=undefined -fno-sanitize-trap=undefined

test: $(GEN)/tests
	$(GEN)/tests

$(GEN)/tests: tests/tests.c common/sha256.c common/tlv.c common/util.c common/jw.c common/netif.c server/httpparse.c $(HDRS)
	@mkdir -p $(GEN)
	$(ZIG) cc $(TEST_CFLAGS) -o $@ tests/tests.c common/sha256.c common/tlv.c common/util.c \
		common/jw.c common/netif.c server/httpparse.c

# libFuzzer targets (allowed to fail where the toolchain lacks libFuzzer support)
fuzz: $(GEN)/fuzz-tlv $(GEN)/fuzz-http
	$(GEN)/fuzz-tlv -runs=200000
	$(GEN)/fuzz-http -runs=200000

$(GEN)/fuzz-tlv: tests/fuzz_tlv.c common/tlv.c
	@mkdir -p $(GEN)
	$(ZIG) cc -std=c11 -D_GNU_SOURCE -g -fsanitize=fuzzer -o $@ $^

$(GEN)/fuzz-http: tests/fuzz_http.c server/httpparse.c
	@mkdir -p $(GEN)
	$(ZIG) cc -std=c11 -D_GNU_SOURCE -g -fsanitize=fuzzer -o $@ $^

lint:
	cppcheck --quiet --error-exitcode=1 --std=c11 --inline-suppr \
		--enable=warning,portability,performance --suppress=missingIncludeSystem \
		-D__linux__ -DLS_VERSION=\"lint\" -I$(GEN) common agent server tools

clean:
	rm -rf build
