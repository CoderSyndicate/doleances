# Minimal-attack-surface runtime image.
#
# The binary is built by the workflow and copied in: there is no toolchain,
# no shell, no package manager and no libc in the final image. Static assets
# are embedded in the binary (//go:embed), so no writable path is required
# and the container can run with a read-only root filesystem.
#
# Pin by digest, not by tag, before the first production push.
FROM gcr.io/distroless/static-debian12:nonroot

ARG SERVICE
ARG TARGETOS
ARG TARGETARCH

COPY dist/${TARGETOS}-${TARGETARCH}/${SERVICE} /service

# Redundant with the :nonroot base, stated explicitly so it survives a base change.
USER 65532:65532

ENTRYPOINT ["/service"]
