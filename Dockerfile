# Moov Mail — moovd container image.
#
# Multi-stage, and deliberately built from the VENDORED tree: `go build -mod=vendor`
# needs no network at all, which means the image that runs next to a real mailbox
# is byte-reproducible from the commit alone and cannot be changed by an upstream
# module disappearing or being retagged. It is also the only honest way to ship
# go-imap/v2, which is pinned by commit and carries Moov's own patch set
# (patches/README.md) — a proxy fetch would silently drop those patches.
#
# The runtime is distroless/static: no shell, no package manager, no libc to
# patch. moovd is a static Go binary, so nothing else is needed, and an attacker
# who reaches code execution finds no tools to pivot with.

# ---------------------------------------------------------------------------
# Build stage
# ---------------------------------------------------------------------------
# Pinned to the same Go series CI builds with (.github/workflows/ci.yml).
FROM golang:1.24-bookworm AS build

WORKDIR /src

# The vendored tree makes the build hermetic, so the whole source is copied in
# one layer: there is no `go mod download` step whose cache would be worth
# preserving separately.
COPY . .

# Build metadata, stamped into internal/version so a running container can be
# traced back to a commit. Passed in by the build command; they default to
# "unknown" rather than failing, so a plain `docker build .` still works.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# CGO is off: it is what makes the binary static, which is what lets the runtime
# stage be distroless/static rather than a distro with a libc.
#
# -trimpath keeps absolute build paths out of the binary (reproducibility and a
# small information leak closed); -w -s drop DWARF and the symbol table, which
# roughly halves the binary and removes nothing a production stack trace needs
# (Go panics carry their own line tables).
ENV CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=vendor
RUN go build \
      -trimpath \
      -ldflags "-w -s \
        -X github.com/GrupoNU/moov/internal/version.Version=${VERSION} \
        -X github.com/GrupoNU/moov/internal/version.Commit=${COMMIT} \
        -X github.com/GrupoNU/moov/internal/version.Date=${BUILD_DATE}" \
      -o /out/moovd ./cmd/moovd \
 && go build \
      -trimpath \
      -ldflags "-w -s \
        -X github.com/GrupoNU/moov/internal/version.Version=${VERSION} \
        -X github.com/GrupoNU/moov/internal/version.Commit=${COMMIT} \
        -X github.com/GrupoNU/moov/internal/version.Date=${BUILD_DATE}" \
      -o /out/moovctl ./cmd/moovctl

# An empty directory for the runtime stage to copy in as the blob root. It has
# to be built HERE because the distroless runtime has no mkdir to make it with.
RUN mkdir -p /blobroot

# ---------------------------------------------------------------------------
# Runtime stage
# ---------------------------------------------------------------------------
# distroless/static: CA certificates (moovd speaks TLS to Dovecot and to the
# Mailcow API), /etc/passwd with a nonroot user, tzdata, and nothing else.
FROM gcr.io/distroless/static-debian12:nonroot

# The blob root, created in the image and OWNED BY nonroot.
#
# This line is load-bearing and was found the hard way. Docker seeds an empty
# named volume from the image's directory at that path — including its owner. If
# the path does not exist in the image, the volume is created root-owned, and a
# container running as uid 65532 then fails its first blob write with EACCES
# while every other part of the daemon looks healthy. Creating it here with the
# right owner makes the volume inherit that ownership instead.
#
# distroless has no shell and no mkdir, so this uses COPY --chown from a stage
# that does have them: build (debian) prepares the empty directory.
COPY --from=build --chown=nonroot:nonroot /blobroot /var/lib/moov/blobs

# Declared so a deployment that forgets to mount a volume gets an anonymous one
# rather than writing blobs into the container's writable layer, where they would
# vanish on the next `docker compose up -d --force-recreate`.
VOLUME ["/var/lib/moov/blobs"]

COPY --from=build /out/moovd /usr/local/bin/moovd
COPY --from=build /out/moovctl /usr/local/bin/moovctl

# nonroot (uid 65532) comes from the base image. moovd binds 8620 and 8080 —
# both above 1024, so no capability is needed to drop.
USER nonroot:nonroot

# Documentation only (nothing is published to the host; the deploy compose keeps
# every port on internal networks — see deploy/README.md).
#   8620  JMAP API (RFC 8620, the mnemonic)
#   8080  operational HTTP: /healthz, /metrics
EXPOSE 8620 8080

ENTRYPOINT ["/usr/local/bin/moovd"]
