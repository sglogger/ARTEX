# syntax=docker/dockerfile:1
#
# Runtime image (nothing is compiled in the image): installs common tools only and adds a **prebuilt Linux single binary**.
# The binary is cross-compiled by CI's binaries job (pure Go, no QEMU) and placed per target architecture at
# dist/<TARGETARCH>/artex in the build context. For multi-arch builds arm64 then only has to emulate the apt layer
# instead of the Next/Go compilation, which is much faster.
#
# When building the image manually, prepare the binary yourself first:
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETOS
ARG TARGETARCH
RUN test "$TARGETOS" = linux && case "$TARGETARCH" in amd64|arm64) ;; *) exit 1 ;; esac
# Common tools: ripgrep / curl / vim, plus a set of recon staples (add or remove as needed).
# Node 22.x comes from NodeSource, matching the frontend CI version.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Preinstall the Playwright MCP and CLI globally so nothing is downloaded with npx at runtime.
# @playwright/mcp: the browser MCP runs `npx @playwright/mcp` directly (installed globally, no -y/@latest needed).
# @playwright/cli: provides playwright-cli; --help is run after install to verify it is executable.
# Then install playwright (browser management) and use --with-deps to preinstall chromium and its system dependencies,
# so MCP/CLI in the container work on first start without downloading a browser.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# Prebuilt binary for the matching architecture (dist/amd64/artex or dist/arm64/artex)
COPY dist/${TARGETARCH}/artex /app/artex
# Supervisor startup script: decides from the exit code whether to relaunch after the process exits; one-click update from the web UI relies on it to swap.
# It also forwards SIGTERM to artex: docker stop only signals PID 1,
# and without forwarding artex never receives it, cannot shut down gracefully, and is hard-killed with SIGKILL after 10 seconds.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# data/ (SQLite + jwt.key) persistence point
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
